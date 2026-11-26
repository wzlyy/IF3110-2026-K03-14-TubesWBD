package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/lock"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/queue"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	ReqStatusDraft             = "draft"
	ReqStatusPendingReview     = "pending_review"
	ReqStatusApproved          = "approved"
	ReqStatusRejected          = "rejected"
	ReqStatusPendingQueue      = "pending_queue"
	ReqStatusRunning           = "running"
	ReqStatusSuccess           = "success"
	ReqStatusFailed            = "failed"
	ReqStatusPartial           = "partial"
	ReqStatusPartiallyReverted = "partially_reverted"
	ReqStatusUncertain         = "uncertain"
)

var (
	ErrRequestNotFound        = errors.New("change request tidak ditemukan")
	ErrInvalidActionType      = errors.New("action_type tidak valid")
	ErrDatabaseBlocked        = errors.New("database sedang tertahan oleh eksekusi partial/uncertain; lakukan resume_batch atau revert_batch terlebih dahulu")
	ErrRollbackNotActiveTip   = errors.New("rollback hanya diizinkan pada migration aktif terakhir (aturan LIFO)")
	ErrIrreversibleMigration  = errors.New("migration ditandai irreversible sehingga tidak dapat di-rollback atau di-revert")
	ErrMigrationAlreadyActive = errors.New("migration ini sudah dalam kondisi aktif/terpasang")
	ErrForwardNotEligible     = errors.New("forward hanya tersedia untuk migration yang pernah di-rollback atau attempt sebelumnya gagal")
	ErrLinearOrderViolation   = errors.New("forward harus mengikuti urutan linear; jalankan forward pada migration sebelumnya terlebih dahulu")
	ErrBatchActionInvalid     = errors.New("aksi batch hanya berlaku pada eksekusi batch dengan status partial/partially_reverted yang sesuai")
	ErrPublisherNotConfigured = errors.New("antrean RabbitMQ belum dikonfigurasi atau tidak tersedia")
)

// MigrationRequest merepresentasikan satu baris pada tabel migration_requests.
type MigrationRequest struct {
	ID                  string     `json:"id"`
	DatabaseID          string     `json:"database_id"`
	DatabaseName        string     `json:"database_name,omitempty"`
	MigrationID         string     `json:"migration_id"`
	MigrationVersion    int        `json:"migration_version"`
	MigrationRevision   int        `json:"migration_revision"`
	MigrationName       string     `json:"migration_name"`
	ExecutionMode       string     `json:"execution_mode"`
	ActionType          string     `json:"action_type"`
	TargetExecutionID   string     `json:"target_execution_id,omitempty"`
	Status              string     `json:"status"`
	RequestedBy         string     `json:"requested_by,omitempty"`
	RequestedByUsername string     `json:"requested_by_username"`
	BatchSizeOverride   *int       `json:"batch_size_override,omitempty"`
	JobID               string     `json:"job_id,omitempty"`
	SQLPreview          string     `json:"sql_preview"`
	IsIrreversible      bool       `json:"is_irreversible"`
	ApprovalStale       bool       `json:"approval_stale"`
	CreatedAt           time.Time  `json:"created_at"`
	SubmittedAt         *time.Time `json:"submitted_at,omitempty"`
	ApprovedAt          *time.Time `json:"approved_at,omitempty"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// RequestDetail menggabungkan MigrationRequest dengan daftar persetujuan leader grupnya.
type RequestDetail struct {
	MigrationRequest
	Approvals []ApprovalRecord `json:"approvals"`
}

// CreateRequestInput adalah payload untuk POST /api/v1/databases/{id}/requests.
type CreateRequestInput struct {
	MigrationID       string `json:"migration_id"`
	ActionType        string `json:"action_type"` // Jenis aksi: up, rollback, forward, resume_batch, revert_batch
	TargetExecutionID string `json:"target_execution_id,omitempty"`
	BatchSizeOverride *int   `json:"batch_size_override,omitempty"`
}

// EnqueueResult adalah respons sinkron (HTTP 202 Accepted) dari POST /api/v1/requests/{id}/enqueue.
type EnqueueResult struct {
	JobID       string `json:"job_id"`
	ExecutionID string `json:"execution_id"`
	RequestID   string `json:"request_id"`
	Status      string `json:"status"`
}

// WorkflowService mengelola change request M2, persetujuan, penjaga lock, dan enqueue asinkron.
type WorkflowService struct {
	DB        *sql.DB
	Locks     *lock.Manager
	Publisher queue.Publisher
}

// CreateRequest memvalidasi aturan linear/LIFO/irreversible/blocking lalu membuat draf request.
func (s *WorkflowService) CreateRequest(ctx context.Context, databaseID, userID string, in CreateRequestInput) (*RequestDetail, error) {
	in.ActionType = strings.ToLower(strings.TrimSpace(in.ActionType))
	in.MigrationID = strings.TrimSpace(in.MigrationID)
	in.TargetExecutionID = strings.TrimSpace(in.TargetExecutionID)

	switch in.ActionType {
	case ActionUp, ActionRollback, ActionForward, ActionResumeBatch, ActionRevertBatch:
	default:
		return nil, fmt.Errorf("%w: gunakan up, rollback, forward, resume_batch, atau revert_batch", ErrInvalidActionType)
	}
	if in.BatchSizeOverride != nil && (*in.BatchSizeOverride <= 0 || *in.BatchSizeOverride > MaxBatchSize) {
		return nil, fmt.Errorf("%w: batch_size_override harus antara 1 dan %d", ErrInvalidDefinition, MaxBatchSize)
	}

	def, err := GetDefinition(ctx, s.DB, databaseID, in.MigrationID)
	if err != nil {
		return nil, err
	}

	allExecs, err := ListAllExecutionsChronological(ctx, s.DB, databaseID)
	if err != nil {
		return nil, err
	}
	state := ComputeActiveState(allExecs)

	// Periksa juga status penahanan distributed lock (held_partial/held_uncertain memblokir request lain).
	var lockInfo *lock.LockInfo
	if s.Locks != nil {
		li, lerr := s.Locks.GetLock(ctx, databaseID)
		if lerr == nil {
			lockInfo = li
		} else if !errors.Is(lerr, lock.ErrLockNotFound) {
			return nil, lerr
		}
	}

	if err := s.ValidateRequestPreconditions(ctx, databaseID, def, in, state, lockInfo); err != nil {
		return nil, err
	}

	targetExecID := in.TargetExecutionID
	if in.ActionType == ActionRollback && targetExecID == "" && state.ActiveTip != nil {
		targetExecID = state.ActiveTip.ExecutionID
	}

	reqID := uuid.NewString()
	_, err = s.DB.ExecContext(ctx, `
		INSERT INTO migration_requests (
			id, database_id, migration_id, action_type, target_execution_id,
			status, requested_by, batch_size_override
		) VALUES ($1, $2, $3, $4, $5, 'draft', $6, $7)
	`, reqID, databaseID, def.ID, in.ActionType, nullableString(targetExecID), nullableString(userID), in.BatchSizeOverride)
	if err != nil {
		return nil, fmt.Errorf("insert migration request: %w", err)
	}

	telemetry.LogAudit(ctx, "migration.request.create", "draft",
		slog.String("request_id", reqID),
		slog.String("database_id", databaseID),
		slog.String("migration_id", def.ID),
		slog.String("action_type", in.ActionType),
	)
	return GetRequestDetail(ctx, s.DB, reqID)
}

func (s *WorkflowService) ValidateRequestPreconditions(
	ctx context.Context,
	databaseID string,
	def *Definition,
	in CreateRequestInput,
	state ActiveState,
	lockInfo *lock.LockInfo,
) error {
	// 1. Periksa status pemblokir (partial, partially_reverted, atau uncertain).
	if lockInfo != nil && lockInfo.HoldState == lock.StateHeldUncertain {
		return ErrDatabaseBlocked
	}
	if (in.ActionType == ActionResumeBatch || in.ActionType == ActionRevertBatch) && state.BlockingExecution == nil {
		return fmt.Errorf("%w: eksekusi partial sudah diselesaikan", ErrBatchActionInvalid)
	}
	if state.BlockingExecution != nil {
		blocking := state.BlockingExecution
		if blocking.Status == ExecStatusUncertain {
			return ErrDatabaseBlocked
		}
		if blocking.Status == ExecStatusPartiallyReverted {
			// Pembatalan tidak aman: izinkan revert_batch pada eksekusi pemblokir ATAU migration perbaikan baru (up).
			if in.ActionType != ActionRevertBatch && in.ActionType != ActionUp {
				return ErrDatabaseBlocked
			}
			if in.ActionType == ActionRevertBatch && (in.TargetExecutionID == "" || in.TargetExecutionID != blocking.ExecutionID) {
				return fmt.Errorf("%w: target_execution_id harus merujuk ke eksekusi partial %s", ErrBatchActionInvalid, blocking.ExecutionID)
			}
		} else {
			// Hanya resume_batch atau revert_batch untuk eksekusi partial yang boleh lanjut.
			if in.ActionType != ActionResumeBatch && in.ActionType != ActionRevertBatch {
				return ErrDatabaseBlocked
			}
			if in.TargetExecutionID == "" || in.TargetExecutionID != blocking.ExecutionID {
				return fmt.Errorf("%w: target_execution_id harus merujuk ke eksekusi partial %s", ErrBatchActionInvalid, blocking.ExecutionID)
			}
		}
	}

	switch in.ActionType {
	case ActionUp:
		if state.AppliedMigrationIDs[def.ID] {
			return ErrMigrationAlreadyActive
		}
		if state.RolledBackMigrations[def.ID] {
			return fmt.Errorf("%w: gunakan action 'forward' untuk menjalankan ulang migration yang pernah di-rollback", ErrInvalidActionType)
		}
		allDefs, err := ListDefinitions(ctx, s.DB, databaseID)
		if err != nil {
			return err
		}
		// Saat membuat request baru, migrasi pendahulu yang sudah memiliki request approved/queued/running
		// diizinkan sebagai antrean berurutan; worker tetap menegakkan ValidateLinearNext saat eksekusi.
		plannedState := state
		rows, qerr := s.DB.QueryContext(ctx, `
			SELECT DISTINCT migration_id
			FROM migration_requests
			WHERE database_id = $1 AND action_type IN ('up', 'forward') AND status IN ('approved', 'pending_queue', 'running')
		`, databaseID)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		appliedCopy := make(map[string]bool, len(state.AppliedMigrationIDs))
		for k, v := range state.AppliedMigrationIDs {
			appliedCopy[k] = v
		}
		for rows.Next() {
			var mid string
			if serr := rows.Scan(&mid); serr != nil {
				return serr
			}
			if mid != def.ID {
				appliedCopy[mid] = true
			}
		}
		if rerr := rows.Err(); rerr != nil {
			return rerr
		}
		plannedState.AppliedMigrationIDs = appliedCopy
		return ValidateLinearNext(*def, allDefs, plannedState)

	case ActionRollback:
		if def.IsIrreversible {
			return fmt.Errorf("%w: %s", ErrIrreversibleMigration, def.IrreversibleReason)
		}
		if state.ActiveTip == nil || state.ActiveTip.MigrationID != def.ID {
			return ErrRollbackNotActiveTip
		}
		if in.TargetExecutionID != "" && in.TargetExecutionID != state.ActiveTip.ExecutionID {
			return ErrRollbackNotActiveTip
		}
		return nil

	case ActionForward:
		allDefs, err := ListDefinitions(ctx, s.DB, databaseID)
		if err != nil {
			return err
		}
		return ValidateLinearForward(*def, allDefs, state)

	case ActionResumeBatch:
		if def.ExecutionMode != ModeBatch {
			return fmt.Errorf("%w: resume_batch hanya untuk migration mode batch", ErrBatchActionInvalid)
		}
		if in.TargetExecutionID == "" {
			return fmt.Errorf("%w: target_execution_id wajib diisi", ErrBatchActionInvalid)
		}
		target, _, err := GetExecutionDetail(ctx, s.DB, in.TargetExecutionID)
		if err != nil {
			return err
		}
		if target.DatabaseID != databaseID || target.MigrationID != def.ID || target.Status != ExecStatusPartial {
			return fmt.Errorf("%w: eksekusi sasaran harus berstatus 'partial'", ErrBatchActionInvalid)
		}
		return nil

	case ActionRevertBatch:
		if def.ExecutionMode != ModeBatch {
			return fmt.Errorf("%w: revert_batch hanya untuk migration mode batch", ErrBatchActionInvalid)
		}
		if def.IsIrreversible {
			return fmt.Errorf("%w: %s", ErrIrreversibleMigration, def.IrreversibleReason)
		}
		if in.TargetExecutionID == "" {
			return fmt.Errorf("%w: target_execution_id wajib diisi", ErrBatchActionInvalid)
		}
		target, _, err := GetExecutionDetail(ctx, s.DB, in.TargetExecutionID)
		if err != nil {
			return err
		}
		if target.DatabaseID != databaseID || target.MigrationID != def.ID ||
			(target.Status != ExecStatusPartial && target.Status != ExecStatusPartiallyReverted) {
			return fmt.Errorf("%w: revert_batch hanya untuk eksekusi berstatus 'partial' atau 'partially_reverted'", ErrBatchActionInvalid)
		}
		return nil
	}
	return nil
}

// EnqueueRequest memverifikasi persetujuan leader aktif, menyimpan status pending_queue,
// lalu mengirim job AMQP persisten beserta konteks trace OTel.
func (s *WorkflowService) EnqueueRequest(ctx context.Context, requestID, actorUserID string) (*EnqueueResult, error) {
	ctx, span := telemetry.Tracer().Start(ctx, "api.migration.enqueue",
		trace.WithAttributes(attribute.String("migration.request_id", requestID)),
	)
	defer span.End()

	if s.Publisher == nil {
		return nil, ErrPublisherNotConfigured
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var (
		databaseID        string
		migrationID       string
		actionType        string
		targetExecID      sql.NullString
		status            string
		execMode          string
		batchSizeOverride sql.NullInt32
	)
	err = tx.QueryRowContext(ctx, `
		SELECT r.database_id, r.migration_id, r.action_type, r.target_execution_id,
		       r.status, d.execution_mode, r.batch_size_override
		FROM migration_requests r
		JOIN migration_definitions d ON d.id = r.migration_id
		WHERE r.id = $1
		FOR UPDATE
	`, requestID).Scan(&databaseID, &migrationID, &actionType, &targetExecID, &status, &execMode, &batchSizeOverride)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRequestNotFound
		}
		return nil, err
	}
	if status != ReqStatusApproved {
		return nil, fmt.Errorf("%w: hanya request berstatus 'approved' yang dapat dimasukkan ke queue (status saat ini: %s)", ErrInvalidStateChange, status)
	}

	if err := VerifyCurrentApprovals(ctx, tx, requestID, databaseID); err != nil {
		return nil, err
	}

	// Cegah enqueue migrasi lain saat lock DB berstatus held_partial atau held_uncertain.
	if s.Locks != nil {
		li, lerr := lock.GetLock(ctx, tx, databaseID)
		if lerr == nil && li != nil {
			if li.HoldState == lock.StateHeldUncertain {
				return nil, ErrDatabaseBlocked
			}
			if li.HoldState == lock.StateHeldPartial &&
				((actionType != ActionResumeBatch && actionType != ActionRevertBatch) || targetExecID.String != li.ExecutionID) {
				allowRepair := false
				if actionType == ActionUp && li.ExecutionID != "" {
					var blockerStatus string
					if qerr := tx.QueryRowContext(ctx, `SELECT status FROM migration_executions WHERE execution_id = $1`, li.ExecutionID).Scan(&blockerStatus); qerr == nil && blockerStatus == ExecStatusPartiallyReverted {
						allowRepair = true
					}
				}
				if !allowRepair {
					return nil, ErrDatabaseBlocked
				}
			}
		} else if lerr != nil && !errors.Is(lerr, lock.ErrLockNotFound) {
			return nil, lerr
		}
	}

	var attemptNum int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt_number), 0) + 1 FROM migration_executions WHERE request_id = $1`, requestID).Scan(&attemptNum); err != nil {
		return nil, err
	}

	jobID := uuid.NewString()
	execID := uuid.NewString()

	if _, err := tx.ExecContext(ctx, `
		UPDATE migration_requests
		SET status = 'pending_queue',
		    job_id = $1,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $2
	`, jobID, requestID); err != nil {
		return nil, err
	}

	// Commit status pending_queue sebelum publish ke RabbitMQ agar worker selalu melihat status terbaru.
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	msg := queue.JobMessage{
		JobID:               jobID,
		ExecutionID:         execID,
		RequestID:           requestID,
		DatabaseID:          databaseID,
		DatabaseName:        telemetry.DatabaseFromContext(ctx),
		MigrationID:         migrationID,
		ActionType:          actionType,
		TargetExecutionID:   targetExecID.String,
		ExecutionMode:       execMode,
		RequestedBy:         actorUserID,
		RequestedByUsername: telemetry.ActorFromContext(ctx),
		AttemptNumber:       attemptNum,
		RetryCount:          0,
		EnqueuedAt:          time.Now().UTC(),
	}
	if batchSizeOverride.Valid {
		v := int(batchSizeOverride.Int32)
		msg.BatchSizeOverride = &v
	}

	ctx = telemetry.WithJobMeta(ctx, telemetry.JobLogMeta{
		MigrationID: migrationID,
		JobID:       jobID,
		ExecutionID: execID,
		Attempt:     attemptNum,
	})

	if err := s.Publisher.PublishJob(ctx, msg); err != nil {
		// Kembalikan status ke approved agar pengguna dapat mencoba enqueue ulang dengan aman.
		compCtx, compCancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, _ = s.DB.ExecContext(compCtx, `
			UPDATE migration_requests
			SET status = 'approved', job_id = NULL, updated_at = CURRENT_TIMESTAMP
			WHERE id = $1 AND status = 'pending_queue' AND job_id = $2
		`, requestID, jobID)
		compCancel()

		telemetry.RecordSpanError(span, err)
		return nil, fmt.Errorf("publish job to queue: %w", err)
	}

	telemetry.GlobalMetrics().RecordJobEnqueued(actionType, execMode)
	telemetry.LogAudit(ctx, "migration.request.enqueue", "pending_queue",
		slog.String("request_id", requestID),
		slog.String("database_id", databaseID),
		slog.String("action_type", actionType),
		slog.String("execution_mode", execMode),
	)

	return &EnqueueResult{
		JobID:       jobID,
		ExecutionID: execID,
		RequestID:   requestID,
		Status:      ReqStatusPendingQueue,
	}, nil
}

const requestColumns = `
	r.id, r.database_id, COALESCE(dc.name, ''), r.migration_id,
	d.version, d.revision, d.name, d.execution_mode,
	r.action_type, COALESCE(r.target_execution_id, ''), r.status,
	COALESCE(r.requested_by, ''), COALESCE(u.username, 'system'),
	r.batch_size_override, COALESCE(r.job_id, ''),
	CASE
		WHEN r.action_type IN ('resume_batch', 'revert_batch') THEN (
			WITH RECURSIVE lineage AS (
				SELECT e.execution_id, e.parent_execution_id, e.action_type, e.executed_sql_snapshot
				FROM migration_executions e WHERE e.execution_id = r.target_execution_id
				UNION ALL
				SELECT e.execution_id, e.parent_execution_id, e.action_type, e.executed_sql_snapshot
				FROM migration_executions e JOIN lineage l ON e.execution_id = l.parent_execution_id
			)
			SELECT CASE
				WHEN r.action_type = 'resume_batch' THEN executed_sql_snapshot
				WHEN action_type = 'rollback' THEN d.sql_up
				ELSE COALESCE(d.sql_down, '')
			END FROM lineage WHERE parent_execution_id IS NULL
		)
		WHEN r.action_type = 'rollback' THEN COALESCE(d.sql_down, '')
		ELSE d.sql_up
	END,
	d.is_irreversible, r.created_at, r.submitted_at, r.approved_at, r.updated_at
`

func scanRequest(row interface{ Scan(...any) error }) (*MigrationRequest, error) {
	var req MigrationRequest
	var batchOverride sql.NullInt32
	if err := row.Scan(
		&req.ID, &req.DatabaseID, &req.DatabaseName, &req.MigrationID,
		&req.MigrationVersion, &req.MigrationRevision, &req.MigrationName, &req.ExecutionMode,
		&req.ActionType, &req.TargetExecutionID, &req.Status,
		&req.RequestedBy, &req.RequestedByUsername,
		&batchOverride, &req.JobID,
		&req.SQLPreview, &req.IsIrreversible,
		&req.CreatedAt, &req.SubmittedAt, &req.ApprovedAt, &req.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRequestNotFound
		}
		return nil, err
	}
	if batchOverride.Valid {
		v := int(batchOverride.Int32)
		req.BatchSizeOverride = &v
	}
	return &req, nil
}

// GetRequestDetail mengambil satu change request beserta persetujuan leader grupnya.
func GetRequestDetail(ctx context.Context, db *sql.DB, requestID string) (*RequestDetail, error) {
	req, err := scanRequest(db.QueryRowContext(ctx, `
		SELECT `+requestColumns+`
		FROM migration_requests r
		JOIN migration_definitions d ON d.id = r.migration_id
		LEFT JOIN database_connections dc ON dc.id = r.database_id
		LEFT JOIN users u ON u.id = r.requested_by
		WHERE r.id = $1
	`, requestID))
	if err != nil {
		return nil, err
	}
	approvals, err := ListApprovalsForRequest(ctx, db, requestID)
	if err != nil {
		return nil, err
	}
	stale, err := IsRequestApprovalStale(ctx, db, req.ID, req.DatabaseID, req.Status)
	if err != nil {
		return nil, err
	}
	req.ApprovalStale = stale
	return &RequestDetail{
		MigrationRequest: *req,
		Approvals:        approvals,
	}, nil
}

// ListRequestsForUser mengembalikan daftar change request pada database yang dapat diakses pengguna.
func ListRequestsForUser(ctx context.Context, db *sql.DB, userID, databaseID string) ([]RequestDetail, error) {
	query := `
		SELECT ` + requestColumns + `
		FROM migration_requests r
		JOIN migration_definitions d ON d.id = r.migration_id
		LEFT JOIN database_connections dc ON dc.id = r.database_id
		LEFT JOIN users u ON u.id = r.requested_by
		WHERE EXISTS (
			SELECT 1
			FROM group_database_grants gdg
			JOIN groups g ON g.id = gdg.group_id
			LEFT JOIN user_groups ug ON ug.group_id = gdg.group_id AND ug.user_id = $1
			WHERE (ug.user_id = $1 OR g.leader_user_id = $1)
			  AND gdg.database_id = r.database_id
			  AND gdg.access_level IN ('VIEW_ONLY', 'VIEW_EDIT')
		)
	`
	args := []any{userID}
	if databaseID != "" {
		query += ` AND r.database_id = $2`
		args = append(args, databaseID)
	}
	query += ` ORDER BY r.created_at DESC`

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var base []MigrationRequest
	for rows.Next() {
		req, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		base = append(base, *req)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	requestIDs := make([]string, len(base))
	for i, r := range base {
		requestIDs[i] = r.ID
	}
	approvalsByReq, err := ListApprovalsForRequests(ctx, db, requestIDs)
	if err != nil {
		return nil, err
	}

	details := make([]RequestDetail, 0, len(base))
	for _, r := range base {
		approvals := approvalsByReq[r.ID]
		if approvals == nil {
			approvals = make([]ApprovalRecord, 0)
		}
		stale, err := IsRequestApprovalStale(ctx, db, r.ID, r.DatabaseID, r.Status)
		if err != nil {
			return nil, err
		}
		r.ApprovalStale = stale
		details = append(details, RequestDetail{
			MigrationRequest: r,
			Approvals:        approvals,
		})
	}
	return details, nil
}
