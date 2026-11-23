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
