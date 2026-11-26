//go:build integration

package worker

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/config"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/lock"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/queue"
)

type fixture struct {
	t                        *testing.T
	s                        Service
	target                   *sql.DB
	dbID, user, group, table string
	version                  int
}

func newFixture(t *testing.T, prefix string) *fixture {
	t.Helper()
	if os.Getenv("WBD_TEST_DATABASES") != "1" {
		t.Skip("requires dedicated integration databases")
	}
	cfg, e := config.Load()
	if e != nil {
		t.Fatal(e)
	}
	db, e := database.NewPlatformDB(cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if e = database.MigratePlatformDB(ctx, db); e != nil {
		t.Fatal(e)
	}
	store, e := database.NewConnectionStore(db, cfg.EncryptionKey)
	if e != nil {
		t.Fatal(e)
	}
	m := database.NewTargetManager(store)
	t.Cleanup(func() { m.Close() })
	tag := strings.ReplaceAll(uuid.NewString(), "-", "")
	port, _ := strconv.Atoi(os.Getenv(prefix + "_PORT"))
	c, e := store.Create(ctx, database.Connection{Name: "worker-" + tag, DBMS: "postgres", Host: os.Getenv(prefix + "_HOST"), Port: port, DBName: os.Getenv(prefix + "_NAME"), Username: os.Getenv(prefix + "_USER")}, os.Getenv(prefix+"_PASSWORD"))
	if e != nil {
		t.Fatal(e)
	}
	target, _, e := m.Open(ctx, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	f := &fixture{t: t, s: Service{DB: db, Targets: m, Locks: lock.NewManager(db), WorkerID: "it-worker"}, target: target, dbID: c.ID, user: uuid.NewString(), group: uuid.NewString(), table: "worker_" + tag}
	f.exec(db, "INSERT INTO users(id,username,password_hash) VALUES($1,$2,'test')", f.user, tag)
	f.exec(db, "INSERT INTO groups(id,name,leader_user_id) VALUES($1,$2,$3)", f.group, tag, f.user)
	f.exec(db, "INSERT INTO group_database_grants(group_id,database_id,access_level) VALUES($1,$2,'VIEW_ONLY')", f.group, c.ID)
	f.exec(target, "CREATE TABLE "+f.table+" (id integer PRIMARY KEY, v integer NOT NULL)")
	f.exec(target, "INSERT INTO "+f.table+" SELECT i,0 FROM generate_series(1,8) AS i")
	t.Cleanup(func() {
		target.Exec("DROP TABLE IF EXISTS " + f.table)
		db.Exec("DELETE FROM database_connections WHERE id=$1", f.dbID)
		db.Exec("DELETE FROM groups WHERE id=$1", f.group)
		db.Exec("DELETE FROM users WHERE id=$1", f.user)
	})
	return f
}

func (f *fixture) exec(db *sql.DB, q string, args ...any) {
	f.t.Helper()
	if _, e := db.Exec(q, args...); e != nil {
		f.t.Fatal(e)
	}
}

func (f *fixture) newJob(mode, up string) queue.JobMessage {
	f.t.Helper()
	f.version++
	id := uuid.NewString()
	f.exec(f.s.DB, `INSERT INTO migration_definitions(id,database_id,version,name,sql_up,sql_down,execution_mode,batch_table,batch_cursor_column,batch_size,created_by)
 VALUES($1,$2,$3,'test',$4,$5,$6,$7,'id',2,$8)`, id, f.dbID, f.version, up, "UPDATE "+f.table+" SET v=0", mode, f.table, f.user)
	return f.request(id, mode, "up", "")
}

func (f *fixture) request(def, mode, action, parent string) queue.JobMessage {
	j := queue.JobMessage{ExecutionID: uuid.NewString(), JobID: uuid.NewString(), RequestID: uuid.NewString(), DatabaseID: f.dbID, MigrationID: def, RequestedBy: f.user, ExecutionMode: mode, ActionType: action, AttemptNumber: 1, TargetExecutionID: parent, EnqueuedAt: time.Now()}
	f.exec(f.s.DB, "INSERT INTO migration_requests(id,database_id,migration_id,action_type,status,requested_by,job_id,target_execution_id) VALUES($1,$2,$3,$4,'pending_queue',$5,$6,NULLIF($7,''))", j.RequestID, f.dbID, def, action, f.user, j.JobID, parent)
	f.exec(f.s.DB, "INSERT INTO migration_approvals(id,request_id,group_id,leader_user_id,required_access_level,decision) VALUES($1,$2,$3,$4,'VIEW_ONLY','approved')", uuid.NewString(), j.RequestID, f.group, f.user)
	return j
}

func (f *fixture) run(j queue.JobMessage, want string) {
	f.t.Helper()
	if e := f.s.Process(context.Background(), j); e != nil {
		f.t.Fatal(e)
	}
	var got, req string
	var why sql.NullString
	if e := f.s.DB.QueryRow("SELECT e.status,r.status,e.error_message FROM migration_executions e JOIN migration_requests r ON e.request_id=r.id WHERE execution_id=$1", j.ExecutionID).Scan(&got, &req, &why); e != nil {
		f.t.Fatal(e)
	}
	if got != want || req != want {
		f.t.Fatalf("execution=%s request=%s want %s error=%s", got, req, want, why.String)
	}
}

func (f *fixture) sum(want int) {
	f.t.Helper()
	var n int
	if e := f.target.QueryRow("SELECT sum(v) FROM " + f.table).Scan(&n); e != nil || n != want {
		f.t.Fatalf("sum=%d want %d err=%v", n, want, e)
	}
}

func TestWorkerBatchRestart(t *testing.T) {
	for _, revert := range []bool{false, true} {
		t.Run(map[bool]string{true: "reverse", false: "resume"}[revert], func(t *testing.T) {
			f := newFixture(t, "TARGET_PG")
			f.exec(f.target, "ALTER TABLE "+f.table+" ADD CONSTRAINT stop_at_five CHECK(id<>5 OR v=0)")
			j := f.newJob("batch", "UPDATE "+f.table+" SET v=v+1")
			f.run(j, "partial")
			f.sum(4)
			var held string
			if e := f.s.DB.QueryRow("SELECT hold_state FROM database_locks WHERE database_id=$1", f.dbID).Scan(&held); e != nil || held != "held_partial" {
				t.Fatalf("lock=%s err=%v", held, e)
			}
			f.s.WorkerID = "restarted-worker"
			f.s.Targets.Invalidate(f.dbID)
			// An independent process uses a fresh target pool and the durable checkpoints.
			target, _, e := f.s.Targets.Open(context.Background(), f.dbID)
			if e != nil {
				t.Fatal(e)
			}
			f.target = target
			f.exec(target, "ALTER TABLE "+f.table+" DROP CONSTRAINT stop_at_five")
			if !revert {
				next := f.request(j.MigrationID, "batch", "resume_batch", j.ExecutionID)
				f.run(next, "success")
				f.run(next, "success")
				stale := f.request(j.MigrationID, "batch", "revert_batch", j.ExecutionID)
				f.run(stale, "failed")
				if _, err := f.s.Locks.GetLock(t.Context(), f.dbID); !errors.Is(err, lock.ErrLockNotFound) {
					t.Fatalf("stale continuation recreated lock: %v", err)
				}
				f.sum(8)
				return
			}
			f.exec(target, "UPDATE "+f.table+" SET v=100 WHERE id=1")
			next := f.request(j.MigrationID, "batch", "revert_batch", j.ExecutionID)
			f.run(next, "partially_reverted")
			f.sum(101)
			f.exec(target, "UPDATE "+f.table+" SET v=1 WHERE id=1")
			final := f.request(j.MigrationID, "batch", "revert_batch", next.ExecutionID)
			f.run(final, "success")
			f.sum(0)
			// No stale partial ancestor may block the next migration.
			f.run(f.request(j.MigrationID, "batch", "forward", ""), "success")
		})
	}
}

func TestWorkerExpiredCommitAndDuplicate(t *testing.T) {
	f := newFixture(t, "TARGET_PG")
	j := f.newJob("normal", "UPDATE "+f.table+" SET v=v+1")
	ctx := context.Background()
	a := attempt{s: &f.s, j: j}
	if e := a.load(ctx); e != nil {
		t.Fatal(e)
	}
	lease, e := f.s.Locks.Acquire(ctx, f.dbID, "crashed", j.ExecutionID, 0)
	if e != nil {
		t.Fatal(e)
	}
	a.token = lease.LockToken
	if e = a.record(ctx, "running", nil); e != nil {
		t.Fatal(e)
	}
	if e = a.open(ctx); e != nil {
		t.Fatal(e)
	}
	if e = a.metadata(ctx); e != nil {
		t.Fatal(e)
	}
	if e = a.normal(ctx); e != nil {
		t.Fatal(e)
	}
	f.exec(f.s.DB, "UPDATE database_locks SET expires_at=now()-interval '1 second' WHERE database_id=$1", f.dbID)
	if e = f.s.Process(ctx, j); !errors.Is(e, lock.ErrLockBusy) {
		t.Fatal(e)
	}
	f.run(j, "success")
	f.sum(8)
}

func TestWorkerSerialAndParallel(t *testing.T) {
	f := newFixture(t, "TARGET_PG")
	other := newFixture(t, "TARGET_PG2")
	slow := f.newJob("normal", "SELECT pg_sleep(3); UPDATE "+f.table+" SET v=v+1")
	same := f.newJob("normal", "UPDATE "+f.table+" SET v=v+1")
	cross := other.newJob("normal", "UPDATE "+other.table+" SET v=v+1")
	done := make(chan error, 1)
	go func() { done <- f.s.Process(context.Background(), slow) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, e := f.s.Locks.GetLock(context.Background(), f.dbID); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first worker did not obtain lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e := f.s.Process(context.Background(), same); !errors.Is(e, lock.ErrLockBusy) {
		t.Fatalf("same database was not serialized: %v", e)
	}
	other.run(cross, "success")
	select {
	case e := <-done:
		t.Fatalf("other database failed to overlap, error %v", e)
	default:
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	f.run(same, "success")
	f.sum(16)
	other.sum(8)
}

func TestWorkerRevalidationFailsOnLeaderOrGrantChange(t *testing.T) {
	t.Run("leader changed after approval", func(t *testing.T) {
		f := newFixture(t, "TARGET_PG")
		otherLeader := uuid.NewString()
		f.exec(f.s.DB, "INSERT INTO users(id,username,password_hash) VALUES($1,$2,'test')", otherLeader, "leader-"+strings.ReplaceAll(otherLeader, "-", ""))
		t.Cleanup(func() {
			_, _ = f.s.DB.Exec("DELETE FROM users WHERE id=$1", otherLeader)
		})

		j := f.newJob("normal", "UPDATE "+f.table+" SET v=v+1")
		f.exec(f.s.DB, "UPDATE groups SET leader_user_id=$1 WHERE id=$2", otherLeader, f.group)
		f.run(j, "failed")
		f.sum(0)
		if _, err := f.s.Locks.GetLock(context.Background(), f.dbID); !errors.Is(err, lock.ErrLockNotFound) {
			t.Fatalf("lock remained after revalidation failure: %v", err)
		}
	})

	t.Run("new group granted access after approval", func(t *testing.T) {
		f := newFixture(t, "TARGET_PG")
		j := f.newJob("normal", "UPDATE "+f.table+" SET v=v+1")

		extraGroup := uuid.NewString()
		f.exec(f.s.DB, "INSERT INTO groups(id,name,leader_user_id) VALUES($1,$2,$3)", extraGroup, "extra-"+strings.ReplaceAll(extraGroup, "-", ""), f.user)
		t.Cleanup(func() {
			_, _ = f.s.DB.Exec("DELETE FROM groups WHERE id=$1", extraGroup)
		})
		f.exec(f.s.DB, "INSERT INTO group_database_grants(group_id,database_id,access_level) VALUES($1,$2,'VIEW_ONLY')", extraGroup, f.dbID)

		f.run(j, "failed")
		f.sum(0)
		if _, err := f.s.Locks.GetLock(context.Background(), f.dbID); !errors.Is(err, lock.ErrLockNotFound) {
			t.Fatalf("lock remained after grant change revalidation failure: %v", err)
		}
	})
}

func TestWorkerBatchCrashRecoveryToHeldPartial(t *testing.T) {
	f := newFixture(t, "TARGET_PG")
	ctx := context.Background()
	j := f.newJob("batch", "UPDATE "+f.table+" SET v=v+1")

	a := attempt{s: &f.s, j: j}
	if e := a.load(ctx); e != nil {
		t.Fatal(e)
	}
	lease, e := f.s.Locks.Acquire(ctx, f.dbID, "crashed-batch-worker", j.ExecutionID, 0)
	if e != nil {
		t.Fatal(e)
	}
	a.token = lease.LockToken
	if e = a.record(ctx, "running", nil); e != nil {
		t.Fatal(e)
	}
	if e = a.open(ctx); e != nil {
		t.Fatal(e)
	}
	if e = a.metadata(ctx); e != nil {
		t.Fatal(e)
	}
	b, e := a.batchSpec()
	if e != nil {
		t.Fatal(e)
	}
	var min, max sql.NullString
	if e = a.target.QueryRowContext(ctx, "SELECT min("+b.col+")::text,max("+b.col+")::text FROM "+b.table).Scan(&min, &max); e != nil {
		t.Fatal(e)
	}
	tx, e := a.targetTx(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = a.forwardBatch(ctx, tx, b, min, checkpoint{max: max.String}); e != nil {
		tx.Rollback()
		t.Fatal(e)
	}
	if e = commit(tx); e != nil {
		t.Fatal(e)
	}
	// Crash before syncOne() or finish(), then expire the lease.
	f.exec(f.s.DB, "UPDATE database_locks SET expires_at=now()-interval '1 second' WHERE database_id=$1", f.dbID)

	if e = f.s.Process(ctx, j); !errors.Is(e, lock.ErrLockBusy) {
		t.Fatalf("expected ErrLockBusy after recovering crashed batch worker, got %v", e)
	}
	var execStatus, reqStatus string
	var batchesCommitted int
	var rowsAffected int64
	if e = f.s.DB.QueryRow(`SELECT e.status,r.status,e.total_batches_committed,e.total_rows_affected FROM migration_executions e JOIN migration_requests r ON r.id=e.request_id WHERE e.execution_id=$1`, j.ExecutionID).
		Scan(&execStatus, &reqStatus, &batchesCommitted, &rowsAffected); e != nil {
		t.Fatal(e)
	}
	if execStatus != "partial" || reqStatus != "partial" || batchesCommitted != 1 || rowsAffected != 2 {
		t.Fatalf("unexpected recovered batch state: status=%s/%s batches=%d rows=%d", execStatus, reqStatus, batchesCommitted, rowsAffected)
	}
	var syncedCount int
	if e = f.s.DB.QueryRow(`SELECT count(*) FROM execution_batch_progress WHERE execution_id=$1 AND direction='forward' AND status='committed'`, j.ExecutionID).Scan(&syncedCount); e != nil || syncedCount != 1 {
		t.Fatalf("expected 1 synced batch checkpoint, got %d err=%v", syncedCount, e)
	}
	li, e := f.s.Locks.GetLock(ctx, f.dbID)
	if e != nil || li.HoldState != lock.StateHeldPartial || li.ExecutionID != j.ExecutionID {
		t.Fatalf("unexpected lock after batch crash recovery: %+v err=%v", li, e)
	}

	resume := f.request(j.MigrationID, "batch", "resume_batch", j.ExecutionID)
	f.run(resume, "success")
	f.sum(8)
}

func TestWorkerZeroBatchUnsafeRevertRestoresPartialLock(t *testing.T) {
	f := newFixture(t, "TARGET_PG")
	ctx := context.Background()
	f.exec(f.target, "ALTER TABLE "+f.table+" ADD CONSTRAINT stop_at_five CHECK(id<>5 OR v=0)")

	e1 := f.newJob("batch", "UPDATE "+f.table+" SET v=v+1")
	f.run(e1, "partial")
	f.sum(4)

	// Mutate row 4 (in batch 2, the first batch revert_batch touches) so 0 batches revert.
	f.exec(f.target, "UPDATE "+f.table+" SET v=99 WHERE id=4")
	e2 := f.request(e1.MigrationID, "batch", "revert_batch", e1.ExecutionID)
	f.run(e2, "failed")

	li, e := f.s.Locks.GetLock(ctx, f.dbID)
	if e != nil || li.HoldState != lock.StateHeldPartial || li.ExecutionID != e1.ExecutionID {
		t.Fatalf("0-batch revert should restore held_partial to E1 (%s), got %+v err=%v", e1.ExecutionID, li, e)
	}

	// A resume_batch that also commits 0 new batches must likewise restore held_partial to E1.
	e2r := f.request(e1.MigrationID, "batch", "resume_batch", e1.ExecutionID)
	f.run(e2r, "failed")
	li, e = f.s.Locks.GetLock(ctx, f.dbID)
	if e != nil || li.HoldState != lock.StateHeldPartial || li.ExecutionID != e1.ExecutionID {
		t.Fatalf("0-batch resume should restore held_partial to E1 (%s), got %+v err=%v", e1.ExecutionID, li, e)
	}

	f.exec(f.target, "ALTER TABLE "+f.table+" DROP CONSTRAINT stop_at_five")
	e3 := f.request(e1.MigrationID, "batch", "resume_batch", e1.ExecutionID)
	f.run(e3, "success")
	f.sum(106)
}

func TestWorkerHeldUncertainQueuesAndVerifiesForNextJob(t *testing.T) {
	f := newFixture(t, "TARGET_PG")
	ctx := context.Background()

	e1 := f.newJob("normal", "UPDATE "+f.table+" SET v=10")
	e2 := f.newJob("normal", "UPDATE "+f.table+" SET v=v+1")

	a := attempt{s: &f.s, j: e1}
	if e := a.load(ctx); e != nil {
		t.Fatal(e)
	}
	lease, e := f.s.Locks.Acquire(ctx, f.dbID, "crashed-uncertain-worker", e1.ExecutionID, 0)
	if e != nil {
		t.Fatal(e)
	}
	a.token = lease.LockToken
	if e = a.record(ctx, "running", nil); e != nil {
		t.Fatal(e)
	}
	if e = a.open(ctx); e != nil {
		t.Fatal(e)
	}
	if e = a.metadata(ctx); e != nil {
		t.Fatal(e)
	}
	if e = a.normal(ctx); e != nil {
		t.Fatal(e)
	}
	f.exec(f.s.DB, "UPDATE database_locks SET hold_state='held_uncertain' WHERE database_id=$1", f.dbID)
	f.exec(f.s.DB, "UPDATE migration_executions SET status='uncertain' WHERE execution_id=$1", e1.ExecutionID)
	f.exec(f.s.DB, "UPDATE migration_requests SET status='uncertain' WHERE id=$1", e1.RequestID)

	var realPort int
	if e = f.s.DB.QueryRow("SELECT port FROM database_connections WHERE id=$1", f.dbID).Scan(&realPort); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_, _ = f.s.DB.Exec("UPDATE database_connections SET port=$2 WHERE id=$1", f.dbID, realPort)
	})

	// Simulate target DB outage: E2 must stay pending_queue instead of failing immediately.
	f.exec(f.s.DB, "UPDATE database_connections SET port=1 WHERE id=$1", f.dbID)
	f.s.Targets.Invalidate(f.dbID)
	if err := f.s.Process(ctx, e2); !errors.Is(err, errVerificationPending) {
		t.Fatalf("expected errVerificationPending while target unreachable, got %v", err)
	}
	var e2ReqStatus string
	var e2ExecCount int
	if e = f.s.DB.QueryRow("SELECT status FROM migration_requests WHERE id=$1", e2.RequestID).Scan(&e2ReqStatus); e != nil || e2ReqStatus != "pending_queue" {
		t.Fatalf("E2 request status = %q err=%v, want pending_queue", e2ReqStatus, e)
	}
	if e = f.s.DB.QueryRow("SELECT count(*) FROM migration_executions WHERE execution_id=$1", e2.ExecutionID).Scan(&e2ExecCount); e != nil || e2ExecCount != 0 {
		t.Fatalf("expected 0 execution rows for queued E2, got %d err=%v", e2ExecCount, e)
	}
	if li, err := f.s.Locks.GetLock(ctx, f.dbID); err != nil || li.HoldState != lock.StateHeldUncertain || li.ExecutionID != e1.ExecutionID {
		t.Fatalf("lock should remain held_uncertain on E1, got %+v err=%v", li, err)
	}

	// Restore target DB: next delivery of E2 verifies E1, clears lock, and returns ErrLockBusy for retry.
	f.exec(f.s.DB, "UPDATE database_connections SET port=$2 WHERE id=$1", f.dbID, realPort)
	f.s.Targets.Invalidate(f.dbID)
	reopened, _, e := f.s.Targets.Open(ctx, f.dbID)
	if e != nil {
		t.Fatal(e)
	}
	f.target = reopened
	t.Cleanup(func() {
		_, _ = f.target.Exec("DROP TABLE IF EXISTS " + f.table)
	})
	if err := f.s.Process(ctx, e2); !errors.Is(err, lock.ErrLockBusy) {
		t.Fatalf("expected ErrLockBusy after verifying E1 for E2, got %v", err)
	}
	var e1Status string
	if e = f.s.DB.QueryRow("SELECT status FROM migration_executions WHERE execution_id=$1", e1.ExecutionID).Scan(&e1Status); e != nil || e1Status != "success" {
		t.Fatalf("E1 status = %q err=%v, want success", e1Status, e)
	}
	if _, err := f.s.Locks.GetLock(ctx, f.dbID); !errors.Is(err, lock.ErrLockNotFound) {
		t.Fatalf("expected lock released after E1 verification, got %v", err)
	}

	f.run(e2, "success")
	f.sum(88)
}
