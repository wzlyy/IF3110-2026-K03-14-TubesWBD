//go:build integration

package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/config"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
)

type fixture struct {
	db          *sql.DB
	locks       *sql.DB
	store       *database.ConnectionStore
	manager     *database.TargetManager
	runner      *Runner
	connection  *database.Connection
	target      *sql.DB
	user, table string
}

func migrationFixture(t *testing.T, dbms string) *fixture {
	t.Helper()
	if os.Getenv("WBD_TEST_DATABASES") != "1" {
		t.Skip("use scripts/test-integration.ps1 with dedicated test databases")
	}
	if os.Getenv("PLATFORM_DB_HOST") == "" || os.Getenv("TARGET_PG_HOST") == "" || dbms == "mysql" && os.Getenv("TARGET_MYSQL_HOST") == "" {
		t.Skip("dedicated integration databases not configured")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	root, err := database.NewPlatformDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	schema := "migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = root.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := root.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Error(err)
		}
	})
	pgcfg, err := pgx.ParseConfig(cfg.PlatformDB.DSN())
	if err != nil {
		t.Fatal(err)
	}
	pgcfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*pgcfg)
	locks := stdlib.OpenDB(*pgcfg)
	t.Cleanup(func() { _ = locks.Close() })
	t.Cleanup(func() { _ = db.Close() })
	if err := database.MigratePlatformDB(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	// Startup migration must remain idempotent for existing installations.
	if err := database.MigratePlatformDB(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	store, err := database.NewConnectionStore(db, cfg.EncryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	manager := database.NewTargetManager(store)
	t.Cleanup(func() { _ = manager.Close() })
	prefix := "TARGET_PG_"
	if dbms == "mysql" {
		prefix = "TARGET_MYSQL_"
	}
	port, _ := strconv.Atoi(os.Getenv(prefix + "PORT"))
	c, err := store.Create(context.Background(), database.Connection{Name: "Migration " + dbms, DBMS: dbms, Host: os.Getenv(prefix + "HOST"), Port: port, DBName: os.Getenv(prefix + "NAME"), Username: os.Getenv(prefix + "USER")}, os.Getenv(prefix+"PASSWORD"))
	if err != nil {
		t.Fatal(err)
	}
	target, _, err := manager.Open(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{db: db, locks: locks, store: store, manager: manager, connection: c, target: target, user: uuid.NewString(), table: "m_" + strings.ReplaceAll(uuid.NewString(), "-", "")}
	f.runner = &Runner{DB: db, Locks: locks, Manager: manager}
	if _, err = db.Exec("INSERT INTO users(id,username,password_hash) VALUES($1,$1,'test-only')", f.user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = target.Exec("DROP TABLE IF EXISTS " + f.table) })
	return f
}

func (f *fixture) execute(t *testing.T, query string) *Attempt {
	t.Helper()
	a, err := f.runner.Execute(context.Background(), f.connection.ID, f.user, query, "")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestMigrationCommitPostgresAndMySQL(t *testing.T) {
	for _, dbms := range []string{"postgres", "mysql"} {
		t.Run(dbms, func(t *testing.T) {
			f := migrationFixture(t, dbms)
			a := f.execute(t, "CREATE TABLE "+f.table+"(id int PRIMARY KEY, value varchar(100)); INSERT INTO "+f.table+" VALUES(1, 'a;b')")
			if a.Status != Success || a.Version != 1 || a.FinishedAt == nil {
				t.Fatalf("attempt: %+v", a)
			}
			var value string
			if err := f.target.QueryRow("SELECT value FROM " + f.table + " WHERE id=1").Scan(&value); err != nil || value != "a;b" {
				t.Fatalf("committed data %s: %v", value, err)
			}
			history, err := ListHistory(context.Background(), f.db, f.connection.ID)
			if err != nil || len(history) != 1 || history[0].Executor != f.user {
				t.Fatalf("history: %+v %v", history, err)
			}
		})
	}
}

func TestMigrationRollbackPostgreSQL(t *testing.T) {
	f := migrationFixture(t, "postgres")
	a := f.execute(t, "CREATE TABLE "+f.table+"(id int); INSERT INTO "+f.table+" VALUES(1); INSERT INTO missing_migration_table VALUES(1)")
	if a.Status != Failed || a.PartialChanges {
		t.Fatalf("attempt: %+v", a)
	}
	var exists bool
	if err := f.target.QueryRow("SELECT to_regclass($1) IS NOT NULL", f.table).Scan(&exists); err != nil || exists {
		t.Fatalf("DDL not rolled back: %v %v", exists, err)
	}
}

func TestMigrationFailureHandlingMySQL(t *testing.T) {
	f := migrationFixture(t, "mysql")
	a := f.execute(t, "CREATE TABLE "+f.table+"(id int) ENGINE=InnoDB; INSERT INTO "+f.table+" VALUES(1); INSERT INTO missing_migration_table VALUES(1)")
	if a.Status != Failed || !a.PartialChanges {
		t.Fatalf("partial failure: %+v", a)
	}
	var count int
	if err := f.target.QueryRow("SELECT COUNT(*) FROM " + f.table).Scan(&count); err != nil || count != 0 {
		t.Fatalf("DDL must remain, transactional DML must roll back: %d %v", count, err)
	}
}

func TestLinearVersionIncrementAndRetry(t *testing.T) {
	f := migrationFixture(t, "postgres")
	f.execute(t, "CREATE TABLE "+f.table+"(id int PRIMARY KEY)")
	failed := f.execute(t, "INSERT INTO "+f.table+" VALUES(1),(1)")
	if failed.Status != Failed || failed.Version != 2 {
		t.Fatalf("failed: %+v", failed)
	}
	retry, err := f.runner.Execute(context.Background(), f.connection.ID, f.user, "", failed.ID)
	if err != nil || retry.ID == failed.ID || retry.Version != 2 || retry.Status != Failed {
		t.Fatalf("retry overwrote/advanced audit: %+v %v", retry, err)
	}
	second := f.execute(t, "INSERT INTO "+f.table+" VALUES(2)")
	third := f.execute(t, "INSERT INTO "+f.table+" VALUES(3)")
	if second.Version != 2 || third.Version != 3 {
		t.Fatalf("versions: %d %d", second.Version, third.Version)
	}
	if _, err := f.runner.Execute(context.Background(), f.connection.ID, f.user, "", failed.ID); !errors.Is(err, ErrStaleRetry) {
		t.Fatalf("outdated retry accepted: %v", err)
	}
	history, err := ListHistory(context.Background(), f.db, f.connection.ID)
	if err != nil || len(history) != 5 {
		t.Fatalf("audit: %d %v", len(history), err)
	}
}

func TestPreventConcurrentMigrationExecution(t *testing.T) {
	f := migrationFixture(t, "postgres")
	result := make(chan error, 1)
	go func() {
		_, err := f.runner.Execute(context.Background(), f.connection.ID, f.user, "SELECT pg_sleep(1.5)", "")
		result <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var n int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM migrations WHERE database_id=$1 AND status='PENDING'", f.connection.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("PENDING was not durable before target execution")
		}
		time.Sleep(10 * time.Millisecond)
	}
	otherProcess := &Runner{DB: f.db, Locks: f.locks, Manager: f.manager}
	if _, err := otherProcess.Execute(context.Background(), f.connection.ID, f.user, "SELECT 2", ""); !errors.Is(err, ErrBusy) {
		t.Errorf("concurrent execution: %v", err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestReconcilePendingMigration(t *testing.T) {
	for _, dbms := range []string{"postgres", "mysql"} {
		t.Run(dbms, func(t *testing.T) {
			f := migrationFixture(t, dbms)
			// Simulate platform persistence failure after a real target commit.
			_, err := f.db.Exec(`CREATE FUNCTION prevent_finalize() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'platform unavailable'; END $$;
		CREATE TRIGGER prevent_finalize BEFORE UPDATE ON migrations FOR EACH ROW EXECUTE FUNCTION prevent_finalize()`)
			if err != nil {
				t.Fatal(err)
			}
			a, err := f.runner.Execute(context.Background(), f.connection.ID, f.user, "CREATE TABLE "+f.table+"(id int)", "")
			if !errors.Is(err, ErrPending) || a == nil || a.Status != Pending {
				t.Fatalf("lost finalization: %+v %v", a, err)
			}
			if _, err = f.db.Exec("DROP TRIGGER prevent_finalize ON migrations"); err != nil {
				t.Fatal(err)
			}
			if _, err = f.runner.Execute(context.Background(), f.connection.ID, f.user, "SELECT 1", ""); !errors.Is(err, ErrPending) {
				t.Fatalf("unresolved pending must block: %v", err)
			}
			a, err = f.runner.Reconcile(context.Background(), f.connection.ID, a.ID)
			if err != nil || a.Status != Success {
				t.Fatalf("receipt did not reconcile: %+v %v", a, err)
			}
			// Reconciliation is idempotent and never replays SQL.
			a, err = f.runner.Reconcile(context.Background(), f.connection.ID, a.ID)
			if err != nil || a.Status != Success {
				t.Fatal(err)
			}
		})
	}
}

func TestReconcileInsufficientEvidenceRemainsPending(t *testing.T) {
	f := migrationFixture(t, "postgres")
	if _, err := f.target.Exec("CREATE TABLE " + f.table + "(id int)"); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	query := fmt.Sprintf("CREATE TABLE %s(id int); INSERT INTO %s VALUES(1)", f.table, f.table)
	if _, err := f.db.Exec("INSERT INTO migrations(id,database_id,version,sql_query,executed_by_user_id,status) VALUES($1,$2,1,$3,$4,'PENDING')", id, f.connection.ID, query, f.user); err != nil {
		t.Fatal(err)
	}
	a, err := f.runner.Reconcile(context.Background(), f.connection.ID, id)
	if !errors.Is(err, ErrPending) || a.Status != Pending {
		t.Fatalf("one existing object is insufficient: %+v %v", a, err)
	}
}

func TestMySQLTemporaryDDLRejectedBeforeExecution(t *testing.T) {
	f := migrationFixture(t, "mysql")
	if _, err := f.target.Exec("CREATE TABLE " + f.table + "(id int) ENGINE=InnoDB"); err != nil {
		t.Fatal(err)
	}
	_, err := f.runner.Execute(context.Background(), f.connection.ID, f.user, "INSERT INTO "+f.table+" VALUES(1); CREATE TEMPORARY TABLE "+f.table+"_temp(id int); INSERT INTO missing_migration_table VALUES(1)", "")
	if !errors.Is(err, ErrInvalidSQL) {
		t.Fatalf("temporary DDL must be rejected before any SQL runs: %v", err)
	}
	var count int
	if err := f.target.QueryRow("SELECT COUNT(*) FROM " + f.table).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollbackable INSERT committed: count=%d err=%v", count, err)
	}
}

func TestMigrationAuditWithBoundedPlatformPool(t *testing.T) {
	f := migrationFixture(t, "postgres")
	f.db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	a, err := f.runner.Execute(ctx, f.connection.ID, f.user, "SELECT 1", "")
	if err != nil || a.Status != Success {
		t.Fatalf("platform lock starved durable audit connection: %+v %v", a, err)
	}
}

func TestReconcileCompletionTimeUTC(t *testing.T) {
	for _, dbms := range []string{"postgres", "mysql"} {
		t.Run(dbms, func(t *testing.T) {
			f := migrationFixture(t, dbms)
			f.target.SetMaxOpenConns(1)
			zone, query := "SET TIME ZONE 'Asia/Jakarta'", "SELECT pg_sleep(0.25)"
			if dbms == "mysql" {
				zone, query = "SET time_zone='+07:00'", "SELECT SLEEP(0.25)"
			}
			if _, err := f.target.Exec(zone); err != nil {
				t.Fatal(err)
			}
			defer func() {
				zone = "SET TIME ZONE 'UTC'"
				if dbms == "mysql" {
					zone = "SET time_zone='+00:00'"
				}
				_, _ = f.target.Exec(zone)
			}()
			if _, err := f.db.Exec(`CREATE FUNCTION prevent_finalize() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'platform unavailable'; END $$;
		CREATE TRIGGER prevent_finalize BEFORE UPDATE ON migrations FOR EACH ROW EXECUTE FUNCTION prevent_finalize()`); err != nil {
				t.Fatal(err)
			}
			a, err := f.runner.Execute(context.Background(), f.connection.ID, f.user, query, "")
			if !errors.Is(err, ErrPending) {
				t.Fatalf("expected pending: %v", err)
			}
			if _, err := f.db.Exec("DROP TRIGGER prevent_finalize ON migrations"); err != nil {
				t.Fatal(err)
			}
			a, err = f.runner.Reconcile(context.Background(), f.connection.ID, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if a.FinishedAt == nil || a.FinishedAt.Before(a.StartedAt.Add(200*time.Millisecond)) || a.FinishedAt.After(time.Now().UTC()) {
				t.Fatalf("completion must follow SQL and retain UTC: started=%v finished=%v", a.StartedAt, a.FinishedAt)
			}
		})
	}
}
