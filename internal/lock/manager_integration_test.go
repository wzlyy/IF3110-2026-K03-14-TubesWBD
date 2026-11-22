//go:build integration

package lock_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/config"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/lock"
)

func openLockTestDB(t *testing.T) (*sql.DB, string, string) {
	t.Helper()
	if os.Getenv("WBD_TEST_DATABASES") != "1" {
		t.Skip("set WBD_TEST_DATABASES=1 via scripts/test-integration.ps1")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config load: %v", err)
	}
	db, err := database.NewPlatformDB(cfg)
	if err != nil {
		t.Fatalf("platform db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	if err := database.MigratePlatformDB(ctx, db); err != nil {
		t.Fatalf("migrate platform db: %v", err)
	}
	_, err = db.ExecContext(ctx, `TRUNCATE execution_batch_progress, database_locks, migration_executions, migration_approvals, migration_requests, migration_definitions, sessions, user_groups, group_database_grants, database_connections, groups, users CASCADE`)
	if err != nil {
		t.Fatalf("truncate tables: %v", err)
	}

	userID := uuid.NewString()
	dbID := uuid.NewString()
	migID := uuid.NewString()
	reqID := uuid.NewString()
	execID := uuid.NewString()
	jobID := uuid.NewString()

	mustExec := func(q string, args ...any) {
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("seed exec failed (%s): %v", q, err)
		}
	}
	mustExec(`INSERT INTO users(id, username, password_hash, is_active) VALUES($1, 'lock_tester', 'hash', TRUE)`, userID)
	mustExec(`INSERT INTO database_connections(id, name, dbms_type, host, port, db_name, username, password_encrypted) VALUES($1, 'lock-pg-1', 'postgres', '127.0.0.1', 15433, 'target_test', 'postgres', 'enc')`, dbID)
	mustExec(`INSERT INTO migration_definitions(id, database_id, version, name, sql_up, sql_down, execution_mode, created_by) VALUES($1, $2, 1, 'm1', 'SELECT 1;', 'SELECT 1;', 'normal', $3)`, migID, dbID, userID)
	mustExec(`INSERT INTO migration_requests(id, database_id, migration_id, action_type, status, requested_by, job_id) VALUES($1, $2, $3, 'up', 'running', $4, $5)`, reqID, dbID, migID, userID, jobID)
	mustExec(`INSERT INTO migration_executions(execution_id, request_id, job_id, database_id, migration_id, migration_version, migration_revision, migration_name, action_type, executed_by, executed_sql_snapshot, execution_mode, status) VALUES($1, $2, $3, $4, $5, 1, 1, 'm1', 'up', $6, 'SELECT 1;', 'normal', 'running')`, execID, reqID, jobID, dbID, migID, userID)

	return db, dbID, execID
}

func TestDistributedLockAndCrashRecoveryIntegration(t *testing.T) {
	db, dbID, execID := openLockTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC()
	mgr := lock.NewManager(db)
	mgr.NowFunc = func() time.Time { return now }

	// 1. Pengambilan pertama berhasil; worker pesaing mendapat ErrLockBusy.
	lease, err := mgr.Acquire(ctx, dbID, "worker-A", execID, 30*time.Second)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if _, err := mgr.Acquire(ctx, dbID, "worker-B", execID, 30*time.Second); !errors.Is(err, lock.ErrLockBusy) {
		t.Fatalf("expected ErrLockBusy for competing worker, got %v", err)
	}

	// 2. Heartbeat memperpanjang masa berlaku lease.
	now = now.Add(20 * time.Second)
	if err := mgr.RenewHeartbeat(ctx, dbID, lease.LockToken, 30*time.Second); err != nil {
		t.Fatalf("RenewHeartbeat failed: %v", err)
	}

	// 3. Simulasikan worker-A crash dengan memajukan waktu melewati TTL.
	now = now.Add(40 * time.Second)
	if _, err := mgr.Acquire(ctx, dbID, "worker-B", execID, 30*time.Second); !errors.Is(err, lock.ErrLockExpired) {
		t.Fatalf("expected ErrLockExpired after worker crash, got %v", err)
	}

	// 4. worker-B menjalankan RecoverExpiredLock sehingga eksekusi dan request menjadi uncertain.
	rec, err := mgr.RecoverExpiredLock(ctx, dbID, "worker-B", 30*time.Second)
	if err != nil {
		t.Fatalf("RecoverExpiredLock failed: %v", err)
	}
	if rec.PreviousExecutionID != execID || rec.PreviousWorkerID != "worker-A" {
		t.Fatalf("unexpected recovered lock: %#v", rec)
	}

	var execStatus string
	if err := db.QueryRowContext(ctx, `SELECT status FROM migration_executions WHERE execution_id = $1`, execID).Scan(&execStatus); err != nil {
		t.Fatal(err)
	}
	if execStatus != "uncertain" {
		t.Fatalf("expected execution status 'uncertain', got %q", execStatus)
	}

	// 5. Selama held_uncertain, pengambilan lock baru ditolak.
	if _, err := mgr.Acquire(ctx, dbID, "worker-C", execID, 30*time.Second); !errors.Is(err, lock.ErrLockHeldUncertain) {
		t.Fatalf("expected ErrLockHeldUncertain, got %v", err)
	}

	// 6. Alihkan ke held_partial dan uji pengambilalihan lewat AcquireForRecoveryOrResume.
	if err := mgr.Hold(ctx, dbID, rec.NewLockToken, lock.StateHeldPartial); err != nil {
		t.Fatalf("Hold held_partial failed: %v", err)
	}
	if _, err := mgr.Acquire(ctx, dbID, "worker-C", execID, 30*time.Second); !errors.Is(err, lock.ErrLockHeldPartial) {
		t.Fatalf("expected ErrLockHeldPartial, got %v", err)
	}
	resumeLease, err := mgr.AcquireForRecoveryOrResume(ctx, dbID, "worker-C", execID, execID, 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireForRecoveryOrResume failed: %v", err)
	}
	if err := mgr.Release(ctx, dbID, resumeLease.LockToken); err != nil {
		t.Fatalf("Release failed: %v", err)
	}
}
