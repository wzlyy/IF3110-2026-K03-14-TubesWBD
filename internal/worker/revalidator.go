package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/lock"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/migration"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/queue"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/telemetry"
)

type Service struct {
	DB        *sql.DB
	Targets   *database.TargetManager
	Locks     *lock.Manager
	WorkerID  string
	processFn func(context.Context, queue.JobMessage) error
}
type attempt struct {
	s                     *Service
	j                     queue.JobMessage
	d                     *migration.Definition
	snapshot, root, token string
	dbName, actor         string
	started               time.Time
	target                *telemetry.InstrumentedDB
}

func (a *attempt) withTelemetry(ctx context.Context) context.Context {
	if a.dbName != "" {
		ctx = telemetry.WithDatabase(ctx, a.dbName)
	}
	if a.actor != "" {
		ctx = telemetry.WithActor(ctx, a.actor)
	}
	if a.j.ExecutionID != "" || a.j.JobID != "" || a.j.MigrationID != "" {
		ctx = telemetry.WithJobMeta(ctx, telemetry.JobLogMeta{
			MigrationID: a.j.MigrationID,
			JobID:       a.j.JobID,
			ExecutionID: a.j.ExecutionID,
			Attempt:     a.j.AttemptNumber,
		})
	}
	return ctx
}

func (a *attempt) continuation() bool {
	return a.j.ActionType == "resume_batch" || a.j.ActionType == "revert_batch"
}

func (a *attempt) load(ctx context.Context) error {
	j := a.j
	r, e := migration.GetRequestDetail(ctx, a.s.DB, j.RequestID)
	if e != nil {
		return e
	}
	if r.JobID != j.JobID || r.DatabaseID != j.DatabaseID || r.MigrationID != j.MigrationID || r.ActionType != j.ActionType || r.TargetExecutionID != j.TargetExecutionID || r.ExecutionMode != j.ExecutionMode {
		return errInvalidJob
	}
	a.dbName = r.DatabaseName
	if a.dbName == "" {
		a.dbName = j.DatabaseName
	}
	a.actor = j.RequestedByUsername
	if a.actor == "" && (j.RequestedBy == "" || j.RequestedBy == r.RequestedBy) {
		a.actor = r.RequestedByUsername
	}
	if a.actor == "" && j.RequestedBy != "" {
		var uname string
		if uerr := a.s.DB.QueryRowContext(ctx, "SELECT username FROM users WHERE id=$1", j.RequestedBy).Scan(&uname); uerr == nil && uname != "" {
			a.actor = uname
		}
	}
	a.j.BatchSizeOverride = r.BatchSizeOverride
	a.d, e = migration.GetDefinition(ctx, a.s.DB, j.DatabaseID, j.MigrationID)
	if e != nil {
		return e
	}
	a.snapshot = a.d.SQLUp
	a.root = j.ExecutionID
	if j.ActionType == "rollback" || j.ActionType == "revert_batch" {
		a.snapshot = a.d.SQLDown
	}
	if a.continuation() {
		var parentSQL string
		e = a.s.DB.QueryRowContext(ctx, `WITH RECURSIVE lineage AS (
 SELECT execution_id, parent_execution_id,executed_sql_snapshot,1 AS depth FROM migration_executions WHERE execution_id=$1 AND database_id=$2 AND migration_id=$3
 UNION ALL SELECT p.execution_id,p.parent_execution_id,p.executed_sql_snapshot,l.depth+1 FROM migration_executions p JOIN lineage l ON p.execution_id=l.parent_execution_id)
 SELECT execution_id,executed_sql_snapshot FROM lineage ORDER BY depth DESC LIMIT 1`, j.TargetExecutionID, j.DatabaseID, j.MigrationID).Scan(&a.root, &parentSQL)
		if e != nil {
			return e
		}
		if j.ActionType == "resume_batch" {
			a.snapshot = parentSQL
		} else {
			var action string
			if e = a.s.DB.QueryRowContext(ctx, "SELECT action_type FROM migration_executions WHERE execution_id=$1", a.root).Scan(&action); e != nil {
				return e
			}
			if action == "rollback" {
				a.snapshot = a.d.SQLUp
			}
		}
	}
	return nil
}

func (a *attempt) validate(ctx context.Context) error {
	j := a.j
	records, err := migration.ListAllExecutionsChronological(ctx, a.s.DB, j.DatabaseID)
	if err != nil {
		return err
	}
	if err := (&migration.WorkflowService{DB: a.s.DB}).ValidateRequestPreconditions(ctx, j.DatabaseID, a.d, migration.CreateRequestInput{MigrationID: j.MigrationID, ActionType: j.ActionType, TargetExecutionID: j.TargetExecutionID}, migration.ComputeActiveState(records), nil); err != nil {
		return err
	}
	return migration.VerifyCurrentApprovals(ctx, a.s.DB, j.RequestID, j.DatabaseID)
}

var errInvalidJob = errors.New("delivery does not match queued request")

func (a *attempt) record(ctx context.Context, status string, cause error) error {
	j := a.j
	tx, e := a.s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	// The revision check protects the snapshot if the draft changed during validation.
	res, e := tx.ExecContext(ctx, "UPDATE migration_definitions SET is_immutable=true WHERE id=$1 AND revision=$2", j.MigrationID, a.d.Revision)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil || n != 1 {
		return fmt.Errorf("definition revision changed before execution")
	}
	traceID, _ := telemetry.TraceIDsFromContext(ctx)
	var parent, reverted, msg, state any
	if a.continuation() {
		parent = j.TargetExecutionID
	}
	if j.ActionType == "rollback" || j.ActionType == "revert_batch" {
		reverted = j.TargetExecutionID
	}
	if cause != nil {
		msg = telemetry.RedactString(cause.Error())
		state = telemetry.ExtractSQLState(cause)
	}
	insRes, e := tx.ExecContext(ctx, `INSERT INTO migration_executions(execution_id,request_id,job_id,database_id,migration_id,migration_version,migration_revision,migration_name,action_type,reverted_execution_id,parent_execution_id,attempt_number,executed_by,executed_sql_snapshot,execution_mode,status,error_message,error_sqlstate,trace_id,finished_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NULLIF($13,''),$14,$15,$16::varchar,$17,$18,$19,CASE WHEN $16::varchar='failed' THEN now() END) ON CONFLICT(execution_id) DO NOTHING`, j.ExecutionID, j.RequestID, j.JobID, j.DatabaseID, j.MigrationID, a.d.Version, a.d.Revision, a.d.Name, j.ActionType, reverted, parent, j.AttemptNumber, j.RequestedBy, a.snapshot, j.ExecutionMode, status, msg, state, traceID)
	if e != nil {
		return e
	}
	inserted, _ := insRes.RowsAffected()
	_, e = tx.ExecContext(ctx, "UPDATE migration_requests SET status=$1,updated_at=now() WHERE id=$2 AND job_id=$3 AND status IN ('pending_queue','running')", status, j.RequestID, j.JobID)
	if e != nil {
		return e
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if status == "failed" && inserted > 0 {
		telemetry.GlobalMetrics().RecordJobFinished(j.ActionType, j.ExecutionMode, "failed", 0)
	}
	telemetry.LogAudit(ctx, "worker.attempt", status,
		slog.String("action_type", j.ActionType), slog.Int("version", a.d.Version),
		slog.Int("revision", a.d.Revision), slog.String("query", a.snapshot),
		slog.Any("error", msg), slog.Any("sqlstate", state))
	return nil
}
