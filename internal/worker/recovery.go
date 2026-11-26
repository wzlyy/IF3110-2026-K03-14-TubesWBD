package worker

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/lock"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/migration"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/queue"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/telemetry"
	"go.opentelemetry.io/otel/trace"
)

func (a *attempt) open(ctx context.Context) error {
	db, kind, e := a.s.Targets.Open(ctx, a.j.DatabaseID)
	if e != nil {
		return e
	}
	if kind != "postgres" {
		return migration.ErrUnsupportedTargetDBMS
	}
	dbLabel := a.dbName
	if dbLabel == "" {
		dbLabel = a.j.DatabaseID
	}
	a.target = telemetry.WrapDB(db, dbLabel)
	return nil
}

// finish records the decision and adjusts the lease atomically on the platform.
func (a *attempt) finish(ctx context.Context, status string, cause error, restoreParent bool) error {
	tx, e := a.s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var token, holdState string
	if e = tx.QueryRowContext(ctx, "SELECT lock_token, hold_state FROM database_locks WHERE database_id=$1 FOR UPDATE", a.j.DatabaseID).Scan(&token, &holdState); e != nil {
		return e
	}
	if token != a.token || holdState == string(lock.StateHeldPartial) {
		return lock.ErrLockNotOwned
	}
	var msg, state any
	if cause != nil {
		msg = telemetry.RedactString(cause.Error())
		state = telemetry.ExtractSQLState(cause)
		if status != "pending_queue" {
			telemetry.RecordSpanError(trace.SpanFromContext(ctx), cause)
		}
	}
	attemptStatus := status
	if status == "pending_queue" {
		attemptStatus = "failed"
	}
	_, e = tx.ExecContext(ctx, "UPDATE migration_executions SET status=$2::varchar,error_message=$3,error_sqlstate=$4,finished_at=CASE WHEN $2::varchar='uncertain' THEN NULL ELSE now() END WHERE execution_id=$1", a.j.ExecutionID, attemptStatus, msg, state)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, "UPDATE migration_requests SET status=$2, updated_at=now() WHERE id=$1", a.j.RequestID, status)
	if e != nil {
		return e
	}
	switch {
	case status == "uncertain":
		_, e = tx.ExecContext(ctx, "UPDATE database_locks SET hold_state='held_uncertain' WHERE database_id=$1", a.j.DatabaseID)
	case status == "partial" || status == "partially_reverted" || restoreParent || (attemptStatus == "failed" && a.repairTarget != ""):
		owner := a.j.ExecutionID
		if restoreParent {
			owner = a.j.TargetExecutionID
		} else if attemptStatus == "failed" && a.repairTarget != "" {
			owner = a.repairTarget
		}
		_, e = tx.ExecContext(ctx, "UPDATE database_locks SET hold_state='held_partial',execution_id=$2 WHERE database_id=$1", a.j.DatabaseID, owner)
	default:
		_, e = tx.ExecContext(ctx, "DELETE FROM database_locks WHERE database_id=$1", a.j.DatabaseID)
	}
	if e != nil {
		return e
	}
	if e = tx.Commit(); e == nil {
		dur := time.Duration(-1)
		if attemptStatus != "uncertain" && !a.started.IsZero() {
			dur = time.Since(a.started)
		}
		telemetry.GlobalMetrics().RecordJobFinished(a.j.ActionType, a.j.ExecutionMode, attemptStatus, dur)
		version, revision := 0, 0
		if a.d != nil {
			version, revision = a.d.Version, a.d.Revision
		}
		telemetry.LogAudit(ctx, "worker.finish", attemptStatus,
			slog.String("action_type", a.j.ActionType),
			slog.Int("version", version),
			slog.Int("revision", revision),
			slog.String("query", a.snapshot),
			slog.Any("error", msg),
			slog.Any("sqlstate", state),
		)
	}
	return e
}

// Inspect proof after acquiring the target transaction fence. When the target is
// unreachable the uncertain state and lock stay durable for the next verifier.
func (a *attempt) inspect(ctx context.Context) (string, error) {
	if a.target == nil {
		if e := a.open(ctx); e != nil {
			return "", e
		}
	}
	if e := a.metadata(ctx); e != nil {
		return "", e
	}
	tx, e := a.targetTx(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	var proof bool
	if e = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM _platform_meta.execution_proofs WHERE execution_id=$1)", a.j.ExecutionID).Scan(&proof); e != nil {
		return "", e
	}
	if a.j.ExecutionMode == "batch" {
		dir := "forward"
		owner := "forward_execution"
		if a.j.ActionType == "revert_batch" || a.j.ActionType == "rollback" {
			dir = "revert"
			owner = "revert_execution"
		}
		rows, e := tx.QueryContext(ctx, "SELECT batch_number,cursor_start,cursor_end,target_max_cursor,rows_affected FROM _platform_meta.batch_checkpoints WHERE execution_id=$1 AND "+owner+"=$2 ORDER BY batch_number", a.root, a.j.ExecutionID)
		if e != nil {
			return "", e
		}
		for rows.Next() {
			var c checkpoint
			if e = rows.Scan(&c.number, &c.start, &c.end, &c.max, &c.rows); e == nil {
				e = a.syncOne(ctx, c, dir)
			}
			if e != nil {
				rows.Close()
				return "", e
			}
		}
		if e = rows.Err(); e != nil {
			return "", e
		}
		rows.Close()
		if !proof {
			var anyBatch, remaining, atMax bool
			var ownCount int
			e = tx.QueryRowContext(ctx, "SELECT count(*)>0,count(*) FILTER (WHERE "+owner+"=$2),coalesce(bool_or(NOT is_reverted),false),coalesce(bool_or(cursor_end=target_max_cursor),false) FROM _platform_meta.batch_checkpoints WHERE execution_id=$1", a.root, a.j.ExecutionID).Scan(&anyBatch, &ownCount, &remaining, &atMax)
			if e != nil {
				return "", e
			}
			if anyBatch {
				if a.continuation() && ownCount == 0 {
					return "failed", nil
				}
				if dir == "revert" {
					if remaining {
						return "partially_reverted", nil
					}
					return "success", nil
				}
				if atMax {
					return "success", nil
				}
				return "partial", nil
			}
		}
	}
	if proof {
		return "success", nil
	}
	return "failed", nil
}

var errVerificationPending = errors.New("target commit evidence is still unavailable")

func (s *Service) verify(ctx context.Context, dbID, execution, token string) (err error) {
	defer func() {
		if err != nil {
			err = errors.Join(errVerificationPending, err)
		}
	}()
	var j queue.JobMessage
	var execStatus string
	var startedAt time.Time
	e := s.DB.QueryRowContext(ctx, `SELECT e.job_id,e.request_id,e.database_id,e.migration_id,e.action_type,e.execution_mode,coalesce(r.target_execution_id,''),e.attempt_number,e.status,e.started_at FROM migration_executions e JOIN migration_requests r ON r.id=e.request_id WHERE e.execution_id=$1`, execution).Scan(&j.JobID, &j.RequestID, &j.DatabaseID, &j.MigrationID, &j.ActionType, &j.ExecutionMode, &j.TargetExecutionID, &j.AttemptNumber, &execStatus, &startedAt)
	if e != nil {
		return e
	}
	if execStatus != "uncertain" && execStatus != "running" {
		return nil
	}
	j.ExecutionID = execution
	a := attempt{s: s, j: j, token: token, started: startedAt}
	if e = a.load(ctx); e != nil {
		return e
	}
	ctx = a.withTelemetry(ctx)
	status, e := a.inspect(ctx)
	if e != nil {
		return e
	}
	var reason error
	if status != "success" {
		reason = errors.New("recovered worker lease using target commit evidence")
	}
	if fe := a.finish(ctx, status, reason, a.continuation() && status == "failed"); fe != nil {
		if errors.Is(fe, sql.ErrNoRows) || errors.Is(fe, lock.ErrLockNotOwned) {
			var curStatus string
			if qerr := s.DB.QueryRowContext(ctx, `SELECT status FROM migration_executions WHERE execution_id=$1`, execution).Scan(&curStatus); qerr == nil && curStatus != "uncertain" && curStatus != "running" {
				return nil
			}
		}
		return fe
	}
	return nil
}

func (s *Service) Process(ctx context.Context, j queue.JobMessage) error {
	var previous, requestStatus string
	e := s.DB.QueryRowContext(ctx, "SELECT e.status,r.status FROM migration_executions e JOIN migration_requests r ON r.id=e.request_id WHERE e.job_id=$1 AND e.execution_id=$2", j.JobID, j.ExecutionID).Scan(&previous, &requestStatus)
	if e == nil && previous == "failed" && requestStatus == "pending_queue" {
		return errNextAttempt
	}
	if e == nil && previous != "running" && previous != "uncertain" {
		return nil
	}
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	a := attempt{s: s, j: j}
	if e = a.load(ctx); e != nil {
		return e
	}
	ctx = a.withTelemetry(ctx)
	lockWaitStart := time.Now()
	if j.LockWaitStartedAt != nil && !j.LockWaitStartedAt.IsZero() {
		lockWaitStart = *j.LockWaitStartedAt
	}
	var lease *lock.Lease
	if a.continuation() {
		lease, e = s.Locks.AcquireForRecoveryOrResume(ctx, j.DatabaseID, s.WorkerID, j.ExecutionID, j.TargetExecutionID, 0)
		if errors.Is(e, lock.ErrLockNotFound) {
			// A crash between taking over the lease and recording the attempt
			// can leave an unresolved parent without a lock.
			records, err := migration.ListAllExecutionsChronological(ctx, s.DB, j.DatabaseID)
			if err != nil {
				return err
			}
			blocker := migration.ComputeActiveState(records).BlockingExecution
			if blocker == nil || blocker.ExecutionID != j.TargetExecutionID {
				return a.record(ctx, "failed", migration.ErrBatchActionInvalid)
			}
			lease, e = s.Locks.Acquire(ctx, j.DatabaseID, s.WorkerID, j.ExecutionID, 0)
		}
	} else {
		lease, e = s.Locks.Acquire(ctx, j.DatabaseID, s.WorkerID, j.ExecutionID, 0)
	}
	if errors.Is(e, lock.ErrLockExpired) {
		rec, re := s.Locks.RecoverExpiredLock(ctx, j.DatabaseID, s.WorkerID, 0)
		if re != nil {
			return re
		}
		if !rec.NoExecutionStarted {
			if re = s.verify(ctx, j.DatabaseID, rec.PreviousExecutionID, rec.NewLockToken); re != nil {
				return re
			}
		}
		return lock.ErrLockBusy
	}
	if errors.Is(e, lock.ErrLockHeldPartial) && j.ActionType == migration.ActionUp {
		if info, le := s.Locks.GetLock(ctx, j.DatabaseID); le == nil && info.HoldState == lock.StateHeldPartial {
			canRepair, blockerMigID, blockerVer, qerr := migration.CanExecutionBeRepaired(ctx, s.DB, j.DatabaseID, info.ExecutionID)
			if qerr == nil && canRepair && a.d.ID != blockerMigID && a.d.Version > blockerVer {
				lease, e = s.Locks.AcquireForRecoveryOrResume(ctx, j.DatabaseID, s.WorkerID, j.ExecutionID, info.ExecutionID, 0)
				if e == nil {
					a.repairTarget = info.ExecutionID
				}
			}
		}
	}
	if errors.Is(e, lock.ErrLockHeldUncertain) || errors.Is(e, lock.ErrLockHeldPartial) || a.continuation() && errors.Is(e, lock.ErrLockBusy) {
		info, le := s.Locks.GetLock(ctx, j.DatabaseID)
		if le != nil {
			return le
		}
		if info.HoldState == lock.StateHeldUncertain {
			if ve := s.verify(ctx, j.DatabaseID, info.ExecutionID, info.LockToken); ve != nil {
				return ve
			}
			if info.ExecutionID == j.ExecutionID {
				return nil
			}
			return lock.ErrLockBusy
		}
		if info.HoldState != lock.StateRunning {
			if info.ExecutionID == j.ExecutionID {
				return nil
			}
			return a.record(ctx, "failed", e)
		}
	}
	if e != nil {
		return e
	}
	a.token = lease.LockToken
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		ticker := time.NewTicker(lock.DefaultHeartbeatPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				beat, stop := context.WithTimeout(ctx, 5*time.Second)
				err := s.Locks.RenewHeartbeat(beat, j.DatabaseID, a.token, lock.DefaultLeaseTTL)
				stop()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	// Duplicate delivery can race the previous owner's atomic finish.
	var rs string
	if e = s.DB.QueryRowContext(ctx, "SELECT status FROM migration_requests WHERE id=$1 AND job_id=$2", j.RequestID, j.JobID).Scan(&rs); e != nil {
		return e
	}
	if rs != "pending_queue" && rs != "running" {
		return s.Locks.Release(ctx, j.DatabaseID, a.token)
	}
	telemetry.GlobalMetrics().ObserveLockWait(time.Since(lockWaitStart))
	check := a.validate(ctx)
	a.started = time.Now()
	if e = a.record(ctx, "running", nil); e != nil {
		if a.continuation() {
			_, _ = s.DB.ExecContext(ctx, "UPDATE database_locks SET hold_state='held_partial',execution_id=$3 WHERE database_id=$1 AND lock_token=$2", j.DatabaseID, a.token, j.TargetExecutionID)
		} else if a.repairTarget != "" {
			_, _ = s.DB.ExecContext(ctx, "UPDATE database_locks SET hold_state='held_partial',execution_id=$3 WHERE database_id=$1 AND lock_token=$2", j.DatabaseID, a.token, a.repairTarget)
		} else {
			_ = s.Locks.Release(ctx, j.DatabaseID, a.token)
		}
		return e
	}
	if check != nil {
		return a.finish(ctx, "failed", check, a.continuation() && !errors.Is(check, migration.ErrBatchActionInvalid))
	}
	telemetry.GlobalMetrics().RecordJobStarted(j.ActionType, j.ExecutionMode, time.Since(j.EnqueuedAt))
	if e = a.open(ctx); e == nil {
		e = a.metadata(ctx)
	}
	if e == nil {
		var proved bool
		e = a.target.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM _platform_meta.execution_proofs WHERE execution_id=$1)", j.ExecutionID).Scan(&proved)
		if e == nil && !proved {
			if j.ExecutionMode == "batch" {
				e = a.batch(ctx)
			} else {
				e = a.normal(ctx)
			}
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	status := "success"
	if e != nil {
		if errors.Is(e, errUncertain) {
			if fe := a.finish(ctx, "uncertain", e, false); fe != nil {
				return fe
			}
			return s.verify(ctx, j.DatabaseID, j.ExecutionID, a.token)
		}
		status = "failed"
		if j.ExecutionMode == "batch" && a.target != nil {
			verified, ve := a.inspect(ctx)
			if ve != nil {
				if fe := a.finish(ctx, "uncertain", e, false); fe != nil {
					return fe
				}
				return errors.Join(errVerificationPending, ve)
			}
			status = verified
		}
		if status == "failed" && isTransient(e) && j.RetryCount < queue.MaxTransientRetries {
			if le := a.finish(ctx, "pending_queue", e, a.continuation()); le != nil {
				return le
			}
			return errors.Join(errNextAttempt, e)
		}
	}
	if fe := a.finish(ctx, status, e, a.continuation() && status == "failed"); fe != nil {
		return fe
	}
	return nil
}
