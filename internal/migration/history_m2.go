package migration

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"
)

const (
	ActionUp          = "up"
	ActionRollback    = "rollback"
	ActionForward     = "forward"
	ActionResumeBatch = "resume_batch"
	ActionRevertBatch = "revert_batch"

	ExecStatusRunning           = "running"
	ExecStatusSuccess           = "success"
	ExecStatusFailed            = "failed"
	ExecStatusPartial           = "partial"
	ExecStatusPartiallyReverted = "partially_reverted"
	ExecStatusUncertain         = "uncertain"
)

var (
	ErrExecutionNotFound = errors.New("riwayat eksekusi migration tidak ditemukan")
	ErrJobNotFound       = errors.New("job migration tidak ditemukan")
)

// ExecutionRecord merepresentasikan satu percobaan eksekusi append-only di migration_executions.
type ExecutionRecord struct {
	ExecutionID           string     `json:"execution_id"`
	RequestID             string     `json:"request_id"`
	JobID                 string     `json:"job_id"`
	DatabaseID            string     `json:"database_id"`
	MigrationID           string     `json:"migration_id"`
	MigrationVersion      int        `json:"migration_version"`
	MigrationRevision     int        `json:"migration_revision"`
	MigrationName         string     `json:"migration_name"`
	ActionType            string     `json:"action_type"`
	RevertedExecutionID   string     `json:"reverted_execution_id,omitempty"`
	ParentExecutionID     string     `json:"parent_execution_id,omitempty"`
	AttemptNumber         int        `json:"attempt_number"`
	ExecutedBy            string     `json:"executed_by,omitempty"`
	ExecutedByUsername    string     `json:"executed_by_username"`
	ExecutedSQLSnapshot   string     `json:"executed_sql_snapshot"`
	ExecutionMode         string     `json:"execution_mode"`
	Status                string     `json:"status"`
	TotalBatchesCommitted int        `json:"total_batches_committed"`
	TotalRowsAffected     int64      `json:"total_rows_affected"`
	ErrorMessage          string     `json:"error_message,omitempty"`
	ErrorSQLState         string     `json:"error_sqlstate,omitempty"`
	TraceID               string     `json:"trace_id,omitempty"`
	StartedAt             time.Time  `json:"started_at"`
	FinishedAt            *time.Time `json:"finished_at,omitempty"`
}

// BatchProgressRecord menyalin satu checkpoint batch (committed, reverted, atau failed).
type BatchProgressRecord struct {
	ID              string    `json:"id"`
	ExecutionID     string    `json:"execution_id"`
	BatchNumber     int       `json:"batch_number"`
	CursorStart     string    `json:"cursor_start,omitempty"`
	CursorEnd       string    `json:"cursor_end,omitempty"`
	TargetMaxCursor string    `json:"target_max_cursor,omitempty"`
	RowsAffected    int64     `json:"rows_affected"`
	Direction       string    `json:"direction"`
	Status          string    `json:"status"`
	CommittedAt     time.Time `json:"committed_at"`
}

// ActiveState menyimpan status LIFO linear database yang dihitung langsung dari riwayat eksekusi.
type ActiveState struct {
	HiddenRollbackIDs    map[string]struct{} `json:"-"`
	ActiveExecutions     []ExecutionRecord   `json:"active_executions"`
	ActiveTip            *ExecutionRecord    `json:"active_tip,omitempty"`
	AppliedMigrationIDs  map[string]bool     `json:"applied_migration_ids"`
	RolledBackMigrations map[string]bool     `json:"rolled_back_migrations"`
	FailedMigrations     map[string]bool     `json:"failed_migrations"`
	BlockingExecution    *ExecutionRecord    `json:"blocking_execution,omitempty"`
}

// HistoryResponse adalah respons untuk GET /api/v1/databases/{database_id}/history.
type HistoryResponse struct {
	DatabaseID    string            `json:"database_id"`
	HideRollback  bool              `json:"hide_rollback"`
	ActiveTip     *ExecutionRecord  `json:"active_tip,omitempty"`
	Blocking      *ExecutionRecord  `json:"blocking_execution,omitempty"`
	HiddenCount   int               `json:"hidden_count"`
	TotalAttempts int               `json:"total_attempts"`
	Executions    []ExecutionRecord `json:"executions"`
}

// JobStatusView adalah respons untuk GET /api/v1/jobs/{job_id}.
type JobStatusView struct {
	JobID          string                `json:"job_id"`
	RequestID      string                `json:"request_id"`
	DatabaseID     string                `json:"database_id"`
	MigrationID    string                `json:"migration_id"`
	ActionType     string                `json:"action_type"`
	RequestStatus  string                `json:"request_status"`
	Execution      *ExecutionRecord      `json:"execution,omitempty"`
	BatchProgress  []BatchProgressRecord `json:"batch_progress"`
	CanResumeBatch bool                  `json:"can_resume_batch"`
	CanRevertBatch bool                  `json:"can_revert_batch"`
	SubmittedAt    *time.Time            `json:"submitted_at,omitempty"`
	ApprovedAt     *time.Time            `json:"approved_at,omitempty"`
	UpdatedAt      time.Time             `json:"updated_at"`
}

// ComputeHiddenRollbackIDs mencari pasangan (execution_id dibatalkan, rollback sukses) berdasarkan execution_id.
// Rollback gagal dan percobaan apply gagal tidak pernah disembunyikan.
func ComputeHiddenRollbackIDs(records []ExecutionRecord) map[string]struct{} {
	hidden, _ := rollbackEffects(records)
	return hidden
}

// A continuation completes or undoes the original operation, even after several retries.
func rollbackEffects(records []ExecutionRecord) (map[string]struct{}, map[string]bool) {
	byID := make(map[string]ExecutionRecord, len(records))
	for _, r := range records {
		byID[r.ExecutionID] = r
	}
	hidden := make(map[string]struct{})
	rolledBack := make(map[string]bool)
	for _, r := range records {
		if r.Status != ExecStatusSuccess {
			continue
		}
		root := r
		if r.ActionType == ActionResumeBatch || r.ActionType == ActionRevertBatch {
			for root.ParentExecutionID != "" {
				root = byID[root.ParentExecutionID]
			}
		}
		var target string
		switch {
		case r.ActionType == ActionRevertBatch:
			target = r.RevertedExecutionID
			if root.ActionType != ActionRollback {
				rolledBack[r.MigrationID] = true
			}
		case root.ActionType == ActionRollback:
			target = root.RevertedExecutionID
			rolledBack[r.MigrationID] = true
		}
		if target != "" {
			hidden[r.ExecutionID] = struct{}{}
			hidden[target] = struct{}{}
		}
	}
	return hidden, rolledBack
}

// FilterHistory menerapkan filter Hide rollback tanpa menghapus data riwayat.
func FilterHistory(records []ExecutionRecord, hideRollback bool) ([]ExecutionRecord, int) {
	if !hideRollback {
		out := make([]ExecutionRecord, len(records))
		copy(out, records)
		return out, 0
	}
	hidden := ComputeHiddenRollbackIDs(records)
	filtered := make([]ExecutionRecord, 0, len(records))
	hiddenCount := 0
	for _, r := range records {
		if _, isHidden := hidden[r.ExecutionID]; isHidden {
			hiddenCount++
			continue
		}
		filtered = append(filtered, r)
	}
	return filtered, hiddenCount
}

// ComputeActiveState menghitung tumpukan aktif, Active Tip LIFO, status rollback/gagal,
// dan eksekusi pemblokir (partial/partially_reverted/uncertain) secara kronologis.
func ComputeActiveState(records []ExecutionRecord) ActiveState {
	hidden, rolledBack := rollbackEffects(records)
	state := ActiveState{
		HiddenRollbackIDs:    hidden,
		ActiveExecutions:     make([]ExecutionRecord, 0),
		AppliedMigrationIDs:  make(map[string]bool),
		RolledBackMigrations: rolledBack,
		FailedMigrations:     make(map[string]bool),
	}

	// Lacak eksekusi partial yang sudah diselesaikan oleh resume_batch atau revert_batch sukses berikutnya.
	resolvedPartialIDs := make(map[string]bool)
	parents := make(map[string]string)
	for _, r := range records {
		if r.ParentExecutionID != "" {
			parents[r.ExecutionID] = r.ParentExecutionID
		}
		if r.Status == ExecStatusSuccess {
			if r.ActionType == ActionResumeBatch && r.ParentExecutionID != "" {
				resolvedPartialIDs[r.ParentExecutionID] = true
			}
			if r.ActionType == ActionRevertBatch {
				if r.RevertedExecutionID != "" {
					resolvedPartialIDs[r.RevertedExecutionID] = true
				}
				if r.ParentExecutionID != "" {
					resolvedPartialIDs[r.ParentExecutionID] = true
				}
			}
		}
	}
	// A successful continuation resolves every partial ancestor, including
	// earlier resume/revert attempts that themselves stopped partway through.
	for id := range resolvedPartialIDs {
		for parent := parents[id]; parent != "" && !resolvedPartialIDs[parent]; parent = parents[parent] {
			resolvedPartialIDs[parent] = true
		}
	}

	for _, r := range records {
		switch r.Status {
		case ExecStatusPartial, ExecStatusPartiallyReverted, ExecStatusUncertain:
			if !resolvedPartialIDs[r.ExecutionID] {
				recCopy := r
				state.BlockingExecution = &recCopy
			}
		case ExecStatusFailed:
			if r.ActionType == ActionUp || r.ActionType == ActionForward {
				state.FailedMigrations[r.MigrationID] = true
			}
		case ExecStatusSuccess:
			if r.ActionType == ActionRollback || r.ActionType == ActionRevertBatch {
				continue
			}
			if r.ActionType == ActionUp || r.ActionType == ActionForward || r.ActionType == ActionResumeBatch {
				if _, cancelled := hidden[r.ExecutionID]; !cancelled {
					state.ActiveExecutions = append(state.ActiveExecutions, r)
					state.AppliedMigrationIDs[r.MigrationID] = true
				}
			}
		}
	}

	if len(state.ActiveExecutions) > 0 {
		tip := state.ActiveExecutions[len(state.ActiveExecutions)-1]
		state.ActiveTip = &tip
	}
	return state
}

// ValidateLinearForward memeriksa syarat forward linear untuk targetDef terhadap state saat ini:
// 1. targetDef belum terpasang aktif.
// 2. targetDef pernah di-rollback atau pernah gagal (up/forward).
// 3. Urutan linear: migrasi rollback versi lebih kecil setelah Active Tip harus di-forward dulu.
// 4. Percobaan gagal tidak memblokir migrasi lain (Revisi 2 & 4).
func ValidateLinearForward(targetDef Definition, allDefs []Definition, state ActiveState) error {
	if state.AppliedMigrationIDs[targetDef.ID] {
		return ErrMigrationAlreadyActive
	}
	wasRolledBack := state.RolledBackMigrations[targetDef.ID]
	wasFailed := state.FailedMigrations[targetDef.ID]
	if !wasRolledBack && !wasFailed {
		return ErrForwardNotEligible
	}

	activeTipVersion := 0
	if state.ActiveTip != nil {
		activeTipVersion = state.ActiveTip.MigrationVersion
	}

	sorted := append([]Definition(nil), allDefs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Version < sorted[j].Version })

	for _, d := range sorted {
		if d.Version <= activeTipVersion {
			continue
		}
		if d.ID == targetDef.ID {
			return nil
		}
		// Hanya migrasi rollback yang belum aktif sebelum targetDef yang memaksa urutan forward linear.
		if !state.AppliedMigrationIDs[d.ID] && state.RolledBackMigrations[d.ID] {
			return ErrLinearOrderViolation
		}
	}
	return nil
}

const executionColumns = `
	e.execution_id, e.request_id, e.job_id, e.database_id, e.migration_id,
	e.migration_version, e.migration_revision, e.migration_name, e.action_type,
	COALESCE(e.reverted_execution_id, ''), COALESCE(e.parent_execution_id, ''),
	e.attempt_number, COALESCE(e.executed_by, ''), COALESCE(u.username, 'system'),
	e.executed_sql_snapshot, e.execution_mode, e.status,
	e.total_batches_committed, e.total_rows_affected,
	COALESCE(e.error_message, ''), COALESCE(e.error_sqlstate, ''), COALESCE(e.trace_id, ''),
	e.started_at, e.finished_at
`

func scanExecution(row interface{ Scan(...any) error }) (*ExecutionRecord, error) {
	var r ExecutionRecord
	if err := row.Scan(
		&r.ExecutionID, &r.RequestID, &r.JobID, &r.DatabaseID, &r.MigrationID,
		&r.MigrationVersion, &r.MigrationRevision, &r.MigrationName, &r.ActionType,
		&r.RevertedExecutionID, &r.ParentExecutionID,
		&r.AttemptNumber, &r.ExecutedBy, &r.ExecutedByUsername,
		&r.ExecutedSQLSnapshot, &r.ExecutionMode, &r.Status,
		&r.TotalBatchesCommitted, &r.TotalRowsAffected,
		&r.ErrorMessage, &r.ErrorSQLState, &r.TraceID,
		&r.StartedAt, &r.FinishedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrExecutionNotFound
		}
		return nil, err
	}
	return &r, nil
}

// ListAllExecutionsChronological memuat seluruh percobaan append-only databaseID secara menaik.
func ListAllExecutionsChronological(ctx context.Context, db *sql.DB, databaseID string) ([]ExecutionRecord, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT `+executionColumns+`
		FROM migration_executions e
		LEFT JOIN users u ON u.id = e.executed_by
		WHERE e.database_id = $1
		ORDER BY e.started_at ASC, e.execution_id ASC
	`, databaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := make([]ExecutionRecord, 0)
	for rows.Next() {
		rec, err := scanExecution(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, *rec)
	}
	return records, rows.Err()
}

// ListHistoryM2 mengembalikan riwayat eksekusi migrasi dengan opsi filter Hide rollback.
func ListHistoryM2(ctx context.Context, db *sql.DB, databaseID string, hideRollback bool) (*HistoryResponse, error) {
	all, err := ListAllExecutionsChronological(ctx, db, databaseID)
	if err != nil {
		return nil, err
	}
	state := ComputeActiveState(all)
	filtered, hiddenCount := FilterHistory(all, hideRollback)

	return &HistoryResponse{
		DatabaseID:    databaseID,
		HideRollback:  hideRollback,
		ActiveTip:     state.ActiveTip,
		Blocking:      state.BlockingExecution,
		HiddenCount:   hiddenCount,
		TotalAttempts: len(all),
		Executions:    filtered,
	}, nil
}

// GetExecutionDetail mengembalikan satu catatan eksekusi beserta checkpoint batch-nya.
func GetExecutionDetail(ctx context.Context, db *sql.DB, executionID string) (*ExecutionRecord, []BatchProgressRecord, error) {
	rec, err := scanExecution(db.QueryRowContext(ctx, `
		SELECT `+executionColumns+`
		FROM migration_executions e
		LEFT JOIN users u ON u.id = e.executed_by
		WHERE e.execution_id = $1
	`, executionID))
	if err != nil {
		return nil, nil, err
	}

	batches, err := listBatchProgress(ctx, db, executionID)
	if err != nil {
		return nil, nil, err
	}
	return rec, batches, nil
}

func listBatchProgress(ctx context.Context, db *sql.DB, executionID string) ([]BatchProgressRecord, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, execution_id, batch_number,
		       COALESCE(cursor_start, ''), COALESCE(cursor_end, ''), COALESCE(target_max_cursor, ''),
		       rows_affected, direction, status, committed_at
		FROM execution_batch_progress
		WHERE execution_id = $1
		ORDER BY direction ASC, batch_number ASC
	`, executionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]BatchProgressRecord, 0)
	for rows.Next() {
		var b BatchProgressRecord
		if err := rows.Scan(
			&b.ID, &b.ExecutionID, &b.BatchNumber,
			&b.CursorStart, &b.CursorEnd, &b.TargetMaxCursor,
			&b.RowsAffected, &b.Direction, &b.Status, &b.CommittedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// GetJobStatus mengembalikan status real-time dan progres batch untuk job_id tertentu.
func GetJobStatus(ctx context.Context, db *sql.DB, jobID string) (*JobStatusView, error) {
	var view JobStatusView
	var isIrreversible bool
	err := db.QueryRowContext(ctx, `
		SELECT r.job_id, r.id, r.database_id, r.migration_id, r.action_type,
		       r.status, r.submitted_at, r.approved_at, r.updated_at, d.is_irreversible
		FROM migration_requests r
		JOIN migration_definitions d ON d.id = r.migration_id
		WHERE r.job_id = $1
	`, jobID).Scan(
		&view.JobID, &view.RequestID, &view.DatabaseID, &view.MigrationID, &view.ActionType,
		&view.RequestStatus, &view.SubmittedAt, &view.ApprovedAt, &view.UpdatedAt, &isIrreversible,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrJobNotFound
		}
		return nil, err
	}
	view.BatchProgress = make([]BatchProgressRecord, 0)

	execRec, err := scanExecution(db.QueryRowContext(ctx, `
		SELECT `+executionColumns+`
		FROM migration_executions e
		LEFT JOIN users u ON u.id = e.executed_by
		WHERE e.job_id = $1
		ORDER BY e.started_at DESC
		LIMIT 1
	`, jobID))
	if err == nil && execRec != nil {
		view.Execution = execRec
		batches, berr := listBatchProgress(ctx, db, execRec.ExecutionID)
		if berr != nil {
			return nil, berr
		}
		view.BatchProgress = batches
		if execRec.ExecutionMode == ModeBatch && (execRec.Status == ExecStatusPartial || execRec.Status == ExecStatusPartiallyReverted) {
			var held bool
			err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM database_locks WHERE database_id=$1 AND execution_id=$2 AND hold_state='held_partial')", view.DatabaseID, execRec.ExecutionID).Scan(&held)
			if err != nil {
				return nil, err
			}
			view.CanResumeBatch = held && execRec.Status == ExecStatusPartial
			view.CanRevertBatch = held && !isIrreversible
		}
	} else if err != nil && !errors.Is(err, ErrExecutionNotFound) {
		return nil, err
	}

	return &view, nil
}
