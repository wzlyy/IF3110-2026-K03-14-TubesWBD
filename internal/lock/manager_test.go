package lock

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// Stub driver SQL dalam memori untuk unit test lock.Manager tanpa PostgreSQL.
type fakeLockDB struct {
	mu    sync.Mutex
	locks map[string]*LockInfo
}

var (
	fakeLockRegistryMu sync.Mutex
	fakeLockInstances  = map[string]*fakeLockDB{}
)

type fakeLockDriver struct{}

func (fakeLockDriver) Open(dsn string) (driver.Conn, error) {
	fakeLockRegistryMu.Lock()
	inst := fakeLockInstances[dsn]
	fakeLockRegistryMu.Unlock()
	if inst == nil {
		return nil, errors.New("unknown fake lock instance")
	}
	return &fakeLockConn{store: inst}, nil
}

type fakeLockConn struct{ store *fakeLockDB }

func (c *fakeLockConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (c *fakeLockConn) Close() error                        { return nil }
func (c *fakeLockConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }

func (c *fakeLockConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()

	switch {
	case strings.Contains(query, "INSERT INTO database_locks"):
		dbID := args[0].Value.(string)
		if _, exists := c.store.locks[dbID]; exists {
			return driver.RowsAffected(0), nil
		}
		c.store.locks[dbID] = &LockInfo{
			DatabaseID:  dbID,
			LockToken:   args[1].Value.(string),
			WorkerID:    args[2].Value.(string),
			ExecutionID: args[3].Value.(string),
			HoldState:   StateRunning,
			AcquiredAt:  args[4].Value.(time.Time),
			HeartbeatAt: args[4].Value.(time.Time),
			ExpiresAt:   args[5].Value.(time.Time),
		}
		return driver.RowsAffected(1), nil

	case strings.Contains(query, "SET heartbeat_at"):
		now := args[0].Value.(time.Time)
		exp := args[1].Value.(time.Time)
		dbID := args[2].Value.(string)
		tok := args[3].Value.(string)
		cur := c.store.locks[dbID]
		if cur == nil || cur.LockToken != tok || cur.HoldState != StateRunning {
			return driver.RowsAffected(0), nil
		}
		cur.HeartbeatAt = now
		cur.ExpiresAt = exp
		return driver.RowsAffected(1), nil

	case strings.Contains(query, "SET hold_state"):
		state := HoldState(args[0].Value.(string))
		now := args[1].Value.(time.Time)
		dbID := args[2].Value.(string)
		tok := args[3].Value.(string)
		cur := c.store.locks[dbID]
		if cur == nil || cur.LockToken != tok {
			return driver.RowsAffected(0), nil
		}
		cur.HoldState = state
		cur.HeartbeatAt = now
		return driver.RowsAffected(1), nil

	case strings.Contains(query, "SET lock_token = $1"):
		newTok := args[0].Value.(string)
		workerID := args[1].Value.(string)
		newExecID := args[2].Value.(string)
		now := args[3].Value.(time.Time)
		exp := args[4].Value.(time.Time)
		dbID := args[5].Value.(string)
		targetExecID := args[6].Value.(string)
		cur := c.store.locks[dbID]
		if cur == nil || cur.HoldState != StateHeldPartial || cur.ExecutionID != targetExecID {
			return driver.RowsAffected(0), nil
		}
		cur.LockToken = newTok
		cur.WorkerID = workerID
		cur.ExecutionID = newExecID
		cur.HoldState = StateRunning
		cur.HeartbeatAt = now
		cur.ExpiresAt = exp
		return driver.RowsAffected(1), nil

	case strings.Contains(query, "DELETE FROM database_locks"):
		dbID := args[0].Value.(string)
		tok := args[1].Value.(string)
		cur := c.store.locks[dbID]
		if cur == nil || cur.LockToken != tok {
			return driver.RowsAffected(0), nil
		}
		delete(c.store.locks, dbID)
		return driver.RowsAffected(1), nil
	}
	return driver.RowsAffected(0), nil
}

func (c *fakeLockConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	if strings.Contains(query, "FROM database_locks") {
		dbID := args[0].Value.(string)
		cur := c.store.locks[dbID]
		if cur == nil {
			return &fakeLockRows{}, nil
		}
		cp := *cur
		return &fakeLockRows{row: &cp}, nil
	}
	return &fakeLockRows{}, nil
}

type fakeLockRows struct {
	row  *LockInfo
	read bool
}

func (r *fakeLockRows) Columns() []string {
	return []string{"database_id", "lock_token", "worker_id", "execution_id", "hold_state", "acquired_at", "heartbeat_at", "expires_at"}
}
func (r *fakeLockRows) Close() error { return nil }
func (r *fakeLockRows) Next(dest []driver.Value) error {
	if r.row == nil || r.read {
		return io.EOF
	}
	r.read = true
	dest[0] = r.row.DatabaseID
	dest[1] = r.row.LockToken
	dest[2] = r.row.WorkerID
	dest[3] = r.row.ExecutionID
	dest[4] = string(r.row.HoldState)
	dest[5] = r.row.AcquiredAt
	dest[6] = r.row.HeartbeatAt
	dest[7] = r.row.ExpiresAt
	return nil
}

func init() {
	sql.Register("fake_lock_pg", fakeLockDriver{})
}

func newFakeLockManager(t *testing.T, name string) (*Manager, *time.Time) {
	t.Helper()
	fakeLockRegistryMu.Lock()
	fakeLockInstances[name] = &fakeLockDB{locks: map[string]*LockInfo{}}
	fakeLockRegistryMu.Unlock()

	db, err := sql.Open("fake_lock_pg", name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	m := NewManager(db)
	m.NowFunc = func() time.Time { return now }
	return m, &now
}

func TestLockLifecycleContentionExpirationAndHold(t *testing.T) {
	mgr, clock := newFakeLockManager(t, "lifecycle")
	ctx := context.Background()

	// 1. Ambil lock DB1 dan DB2 secara paralel.
	lease1, err := mgr.Acquire(ctx, "db-1", "worker-1", "exec-1", 30*time.Second)
	if err != nil {
		t.Fatalf("acquire db-1 failed: %v", err)
	}
	lease2, err := mgr.Acquire(ctx, "db-2", "worker-2", "exec-2", 30*time.Second)
	if err != nil {
		t.Fatalf("acquire db-2 parallel failed: %v", err)
	}
	defer mgr.Release(ctx, "db-2", lease2.LockToken)

	// 2. Pengambilan kedua pada DB1 yang aktif menghasilkan ErrLockBusy.
	if _, err := mgr.Acquire(ctx, "db-1", "worker-2", "exec-3", 30*time.Second); !errors.Is(err, ErrLockBusy) {
		t.Fatalf("expected ErrLockBusy, got %v", err)
	}

	// 3. Perpanjangan dengan token salah gagal; token valid memperpanjang lease.
	if err := mgr.RenewHeartbeat(ctx, "db-1", "wrong-token", 30*time.Second); !errors.Is(err, ErrLockNotOwned) {
		t.Fatalf("expected ErrLockNotOwned, got %v", err)
	}
	*clock = clock.Add(20 * time.Second)
	if err := mgr.RenewHeartbeat(ctx, "db-1", lease1.LockToken, 30*time.Second); err != nil {
		t.Fatalf("RenewHeartbeat failed: %v", err)
	}

	// 4. Majukan waktu melewati TTL sehingga Acquire mengembalikan ErrLockExpired.
	*clock = clock.Add(35 * time.Second)
	if _, err := mgr.Acquire(ctx, "db-1", "worker-2", "exec-3", 30*time.Second); !errors.Is(err, ErrLockExpired) {
		t.Fatalf("expected ErrLockExpired after lease expiry, got %v", err)
	}

	// 5. Status held_partial menahan pengambilan baru meski lewat TTL dan mengizinkan resume.
	if err := mgr.Hold(ctx, "db-1", lease1.LockToken, StateHeldPartial); err != nil {
		t.Fatalf("Hold partial failed: %v", err)
	}
	if _, err := mgr.Acquire(ctx, "db-1", "worker-2", "exec-3", 30*time.Second); !errors.Is(err, ErrLockHeldPartial) {
		t.Fatalf("expected ErrLockHeldPartial, got %v", err)
	}
	resumed, err := mgr.AcquireForRecoveryOrResume(ctx, "db-1", "worker-3", "exec-resume", "exec-1", 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireForRecoveryOrResume failed: %v", err)
	}

	// 5b. Status held_uncertain menahan seluruh pengambilan dengan ErrLockHeldUncertain.
	if err := mgr.Hold(ctx, "db-1", resumed.LockToken, StateHeldUncertain); err != nil {
		t.Fatalf("Hold uncertain failed: %v", err)
	}
	if _, err := mgr.Acquire(ctx, "db-1", "worker-4", "exec-4", 30*time.Second); !errors.Is(err, ErrLockHeldUncertain) {
		t.Fatalf("expected ErrLockHeldUncertain, got %v", err)
	}

	// 6. Release melepas lock dengan bersih.
	if err := mgr.Release(ctx, "db-1", resumed.LockToken); err != nil {
		t.Fatalf("Release failed: %v", err)
	}
	if _, err := mgr.GetLock(ctx, "db-1"); !errors.Is(err, ErrLockNotFound) {
		t.Fatalf("expected ErrLockNotFound after release, got %v", err)
	}
}

func TestStartHeartbeatRenewsAndStopsCleanly(t *testing.T) {
	mgr, _ := newFakeLockManager(t, "heartbeat")
	mgr.NowFunc = func() time.Time { return time.Now().UTC() }
	ctx := context.Background()

	lease, err := mgr.Acquire(ctx, "db-hb", "worker-hb", "exec-hb", 5*time.Second)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	before, err := mgr.GetLock(ctx, "db-hb")
	if err != nil {
		t.Fatal(err)
	}

	stop := mgr.StartHeartbeat(ctx, lease, 5*time.Second, 15*time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	stop()

	after, err := mgr.GetLock(ctx, "db-hb")
	if err != nil {
		t.Fatal(err)
	}
	if !after.ExpiresAt.After(before.ExpiresAt) {
		t.Fatalf("expected heartbeat to extend expires_at: before=%v after=%v", before.ExpiresAt, after.ExpiresAt)
	}
}
