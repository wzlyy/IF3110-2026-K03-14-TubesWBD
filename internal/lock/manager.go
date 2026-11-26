package lock

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// HoldState merepresentasikan status distributed lock untuk sebuah database.
type HoldState string

const (
	StateRunning       HoldState = "running"
	StateHeldPartial   HoldState = "held_partial"
	StateHeldUncertain HoldState = "held_uncertain"

	DefaultLeaseTTL        = 30 * time.Second
	DefaultHeartbeatPeriod = 10 * time.Second
)

var (
	ErrLockBusy          = errors.New("database is currently locked by another active migration")
	ErrLockExpired       = errors.New("database lock lease expired; worker crash recovery required")
	ErrLockHeldPartial   = errors.New("database is locked in held_partial state; resume or revert required")
	ErrLockHeldUncertain = errors.New("database is locked in held_uncertain state; uncertain commit verification required")
	ErrLockNotOwned      = errors.New("lock token does not match current lock owner")
	ErrLockNotFound      = errors.New("database lock not found")
)

// LockInfo menyimpan data satu baris pada tabel database_locks.
type LockInfo struct {
	DatabaseID  string    `json:"database_id"`
	LockToken   string    `json:"lock_token"`
	WorkerID    string    `json:"worker_id"`
	ExecutionID string    `json:"execution_id"`
	HoldState   HoldState `json:"hold_state"`
	AcquiredAt  time.Time `json:"acquired_at"`
	HeartbeatAt time.Time `json:"heartbeat_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// Lease merepresentasikan sewa distributed lock aktif milik worker.
type Lease struct {
	DatabaseID  string
	LockToken   string
	WorkerID    string
	ExecutionID string
	ExpiresAt   time.Time
}

// RecoveredLock menyimpan detail lock kedaluwarsa yang dialihkan ke held_uncertain
// agar worker pemulih dapat mengecek tabel bukti _platform_meta di DB target.
// Jika NoExecutionStarted bernilai true, worker crash sebelum membuat baris eksekusi dan lock telah dibersihkan.
type RecoveredLock struct {
	DatabaseID          string
	NewLockToken        string
	PreviousWorkerID    string
	PreviousExecutionID string
	MigrationID         string
	ExecutionMode       string
	NoExecutionStarted  bool
}

// Manager mengelola distributed lock antar-worker di tabel database_locks pada Platform DB.
type Manager struct {
	DB      *sql.DB
	NowFunc func() time.Time
}

func NewManager(db *sql.DB) *Manager {
	return &Manager{DB: db, NowFunc: func() time.Time { return time.Now().UTC() }}
}

func (m *Manager) now() time.Time {
	if m.NowFunc != nil {
		return m.NowFunc().UTC()
	}
	return time.Now().UTC()
}

// RowQuerier allows lock lookups to share an active transaction or connection pool.
type RowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// GetLock mengambil data lock saat ini untuk database, atau ErrLockNotFound jika tidak terkunci.
func (m *Manager) GetLock(ctx context.Context, databaseID string) (*LockInfo, error) {
	return GetLock(ctx, m.DB, databaseID)
}

// GetLock membaca data lock saat ini menggunakan RowQuerier (*sql.DB atau *sql.Tx).
func GetLock(ctx context.Context, q RowQuerier, databaseID string) (*LockInfo, error) {
	var info LockInfo
	var state string
	err := q.QueryRowContext(ctx, `
		SELECT database_id, lock_token, worker_id, execution_id, hold_state, acquired_at, heartbeat_at, expires_at
		FROM database_locks
		WHERE database_id = $1
	`, databaseID).Scan(
		&info.DatabaseID,
		&info.LockToken,
		&info.WorkerID,
		&info.ExecutionID,
		&state,
		&info.AcquiredAt,
		&info.HeartbeatAt,
		&info.ExpiresAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrLockNotFound
		}
		return nil, fmt.Errorf("query database lock: %w", err)
	}
	info.HoldState = HoldState(state)
	return &info, nil
}

// Acquire mengambil lease eksklusif berstatus running untuk databaseID dan executionID.
// Mengembalikan ErrLockBusy, ErrLockExpired, ErrLockHeldPartial, atau ErrLockHeldUncertain jika tidak tersedia.
func (m *Manager) Acquire(ctx context.Context, databaseID, workerID, executionID string, ttl time.Duration) (*Lease, error) {
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}
	ctx, span := telemetry.Tracer().Start(ctx, "lock.acquire",
		trace.WithAttributes(
			attribute.String("database.id", databaseID),
			attribute.String("worker.id", workerID),
			attribute.String("execution.id", executionID),
		),
	)
	defer span.End()

	now := m.now()
	expiresAt := now.Add(ttl)
	token := uuid.NewString()

	res, err := m.DB.ExecContext(ctx, `
		INSERT INTO database_locks (database_id, lock_token, worker_id, execution_id, hold_state, acquired_at, heartbeat_at, expires_at)
		VALUES ($1, $2, $3, $4, 'running', $5, $5, $6)
		ON CONFLICT (database_id) DO NOTHING
	`, databaseID, token, workerID, executionID, now, expiresAt)
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, fmt.Errorf("insert database lock: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("check lock rows affected: %w", err)
	}
	if rows == 1 {
		telemetry.LogAudit(ctx, "lock.acquire", "ok",
			slog.String("database_id", databaseID),
			slog.String("worker_id", workerID),
			slog.String("hold_state", string(StateRunning)),
		)
		return &Lease{
			DatabaseID:  databaseID,
			LockToken:   token,
			WorkerID:    workerID,
			ExecutionID: executionID,
			ExpiresAt:   expiresAt,
		}, nil
	}

	existing, err := m.GetLock(ctx, databaseID)
	if err != nil {
		if errors.Is(err, ErrLockNotFound) {
			return nil, ErrLockBusy
		}
		return nil, err
	}

	switch existing.HoldState {
	case StateHeldPartial:
		return nil, ErrLockHeldPartial
	case StateHeldUncertain:
		return nil, ErrLockHeldUncertain
	case StateRunning:
		if !existing.ExpiresAt.After(now) {
			return nil, ErrLockExpired
		}
		return nil, ErrLockBusy
	default:
		return nil, ErrLockBusy
	}
}

// AcquireForRecoveryOrResume mengubah lock held_partial milik targetExecutionID
// menjadi lease running aktif untuk eksekusi resume_batch atau revert_batch yang telah disetujui.
func (m *Manager) AcquireForRecoveryOrResume(ctx context.Context, databaseID, workerID, newExecutionID, targetExecutionID string, ttl time.Duration) (*Lease, error) {
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}
	now := m.now()
	expiresAt := now.Add(ttl)
	newToken := uuid.NewString()

	res, err := m.DB.ExecContext(ctx, `
		UPDATE database_locks
		SET lock_token = $1,
		    worker_id = $2,
		    execution_id = $3,
		    hold_state = 'running',
		    heartbeat_at = $4,
		    expires_at = $5
		WHERE database_id = $6
		  AND hold_state = 'held_partial'
		  AND execution_id = $7
	`, newToken, workerID, newExecutionID, now, expiresAt, databaseID, targetExecutionID)
	if err != nil {
		return nil, fmt.Errorf("takeover held_partial lock: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != 1 {
		existing, gerr := m.GetLock(ctx, databaseID)
		if gerr != nil {
			return nil, gerr
		}
		if existing.HoldState == StateHeldUncertain {
			return nil, ErrLockHeldUncertain
		}
		if existing.HoldState == StateRunning && !existing.ExpiresAt.After(now) {
			return nil, ErrLockExpired
		}
		return nil, ErrLockBusy
	}

	telemetry.LogAudit(ctx, "lock.acquire_resume", "ok",
		slog.String("database_id", databaseID),
		slog.String("worker_id", workerID),
		slog.String("target_execution_id", targetExecutionID),
	)
	return &Lease{
		DatabaseID:  databaseID,
		LockToken:   newToken,
		WorkerID:    workerID,
		ExecutionID: newExecutionID,
		ExpiresAt:   expiresAt,
	}, nil
}

// RenewHeartbeat memperpanjang expires_at untuk lease running milik lockToken.
func (m *Manager) RenewHeartbeat(ctx context.Context, databaseID, lockToken string, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}
	now := m.now()
	expiresAt := now.Add(ttl)
	res, err := m.DB.ExecContext(ctx, `
		UPDATE database_locks
		SET heartbeat_at = $1, expires_at = $2
		WHERE database_id = $3 AND lock_token = $4 AND hold_state = 'running'
	`, now, expiresAt, databaseID, lockToken)
	if err != nil {
		return fmt.Errorf("renew lock heartbeat: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrLockNotOwned
	}
	return nil
}

// Hold mengubah lock running menjadi held_partial atau held_uncertain
// agar migrasi lain tetap tertahan hingga resume/revert atau verifikasi selesai.
func (m *Manager) Hold(ctx context.Context, databaseID, lockToken string, state HoldState) error {
	if state != StateHeldPartial && state != StateHeldUncertain {
		return fmt.Errorf("invalid persistent hold state %q", state)
	}
	now := m.now()
	res, err := m.DB.ExecContext(ctx, `
		UPDATE database_locks
		SET hold_state = $1, heartbeat_at = $2
		WHERE database_id = $3 AND lock_token = $4
	`, string(state), now, databaseID, lockToken)
	if err != nil {
		return fmt.Errorf("set lock hold state: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrLockNotOwned
	}
	telemetry.LogAudit(ctx, "lock.hold", "ok",
		slog.String("database_id", databaseID),
		slog.String("hold_state", string(state)),
	)
	return nil
}

// Release melepas distributed lock jika dimiliki oleh lockToken.
func (m *Manager) Release(ctx context.Context, databaseID, lockToken string) error {
	res, err := m.DB.ExecContext(ctx, `
		DELETE FROM database_locks
		WHERE database_id = $1 AND lock_token = $2
	`, databaseID, lockToken)
	if err != nil {
		return fmt.Errorf("release database lock: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrLockNotOwned
	}
	telemetry.LogAudit(ctx, "lock.release", "ok",
		slog.String("database_id", databaseID),
	)
	return nil
}

// RecoverExpiredLock mengambil alih lock kedaluwarsa dari worker yang crash secara atomik,
// mengubahnya ke held_uncertain serta menandai eksekusi dan request sebagai uncertain.
func (m *Manager) RecoverExpiredLock(ctx context.Context, databaseID, recoverWorkerID string, ttl time.Duration) (*RecoveredLock, error) {
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}
	ctx, span := telemetry.Tracer().Start(ctx, "lock.recover_expired",
		trace.WithAttributes(
			attribute.String("database.id", databaseID),
			attribute.String("worker.id", recoverWorkerID),
		),
	)
	defer span.End()

	now := m.now()
	expiresAt := now.Add(ttl)
	newToken := uuid.NewString()

	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin lock recovery tx: %w", err)
	}
	defer tx.Rollback()

	var prevWorker, prevExecID, holdState string
	var prevExpires time.Time
	err = tx.QueryRowContext(ctx, `
		SELECT worker_id, execution_id, hold_state, expires_at
		FROM database_locks
		WHERE database_id = $1
		FOR UPDATE
	`, databaseID).Scan(&prevWorker, &prevExecID, &holdState, &prevExpires)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrLockNotFound
		}
		return nil, fmt.Errorf("lock row query for recovery: %w", err)
	}
	if HoldState(holdState) != StateRunning || prevExpires.After(now) {
		return nil, ErrLockBusy
	}

	var migrationID, execMode, requestID string
	err = tx.QueryRowContext(ctx, `
		SELECT migration_id, execution_mode, request_id
		FROM migration_executions
		WHERE execution_id = $1
	`, prevExecID).Scan(&migrationID, &execMode, &requestID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Worker crash setelah mengambil lock namun sebelum memulai eksekusi target.
			if _, derr := tx.ExecContext(ctx, `DELETE FROM database_locks WHERE database_id = $1`, databaseID); derr != nil {
				return nil, fmt.Errorf("clear stale lock without execution: %w", derr)
			}
			if cerr := tx.Commit(); cerr != nil {
				return nil, fmt.Errorf("commit stale lock cleanup: %w", cerr)
			}
			telemetry.LogAudit(ctx, "lock.recover", "cleared_no_execution",
				slog.String("database_id", databaseID),
				slog.String("previous_worker_id", prevWorker),
				slog.String("previous_execution_id", prevExecID),
			)
			return &RecoveredLock{
				DatabaseID:          databaseID,
				PreviousWorkerID:    prevWorker,
				PreviousExecutionID: prevExecID,
				NoExecutionStarted:  true,
			}, nil
		}
		return nil, fmt.Errorf("lookup previous execution for recovery: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE database_locks
		SET lock_token = $1,
		    worker_id = $2,
		    hold_state = 'held_uncertain',
		    heartbeat_at = $3,
		    expires_at = $4
		WHERE database_id = $5
	`, newToken, recoverWorkerID, now, expiresAt, databaseID); err != nil {
		return nil, fmt.Errorf("update lock to held_uncertain: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE migration_executions
		SET status = 'uncertain',
		    error_message = COALESCE(error_message, 'Worker lease expired; verifying target commit proof')
		WHERE execution_id = $1 AND status = 'running'
	`, prevExecID); err != nil {
		return nil, fmt.Errorf("mark execution uncertain: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE migration_requests
		SET status = 'uncertain', updated_at = $1
		WHERE id = $2 AND status = 'running'
	`, now, requestID); err != nil {
		return nil, fmt.Errorf("mark request uncertain: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit lock recovery: %w", err)
	}

	telemetry.LogAudit(ctx, "lock.recover", "uncertain",
		slog.String("database_id", databaseID),
		slog.String("previous_worker_id", prevWorker),
		slog.String("previous_execution_id", prevExecID),
		slog.String("recover_worker_id", recoverWorkerID),
	)

	return &RecoveredLock{
		DatabaseID:          databaseID,
		NewLockToken:        newToken,
		PreviousWorkerID:    prevWorker,
		PreviousExecutionID: prevExecID,
		MigrationID:         migrationID,
		ExecutionMode:       execMode,
	}, nil
}

// StartHeartbeat menjalankan goroutine perpanjangan lease berkala hingga dihentikan.
func (m *Manager) StartHeartbeat(ctx context.Context, lease *Lease, ttl, interval time.Duration) (stop func()) {
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}
	if interval <= 0 {
		interval = DefaultHeartbeatPeriod
	}
	hbCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				renewCtx, rCancel := context.WithTimeout(hbCtx, 5*time.Second)
				err := m.RenewHeartbeat(renewCtx, lease.DatabaseID, lease.LockToken, ttl)
				rCancel()
				if err != nil {
					return
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
