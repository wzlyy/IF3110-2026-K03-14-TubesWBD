//go:build integration

package migration

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
)

func securityAdminMySQL(t *testing.T) *sql.DB {
	t.Helper()
	cfg := mysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(os.Getenv("TARGET_MYSQL_HOST"), os.Getenv("TARGET_MYSQL_PORT"))
	cfg.DBName = os.Getenv("TARGET_MYSQL_NAME")
	cfg.User = os.Getenv("TARGET_MYSQL_ADMIN_USER")
	if cfg.User == "" {
		cfg.User = "root"
	}
	cfg.Passwd = os.Getenv("TARGET_MYSQL_ADMIN_PASSWORD")
	if cfg.Passwd == "" {
		cfg.Passwd = os.Getenv("TARGET_MYSQL_PASSWORD")
	}
	cfg.ParseTime = true
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		t.Fatal("cannot prepare MySQL provisioning connection")
	}
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatal("MySQL provisioning connection unavailable")
	}
	return db
}

func TestMigrationRejectsUnsafeMySQLCredentialsAcrossDatabases(t *testing.T) {
	f := migrationHTTPFixture(t, "mysql", "VIEW_EDIT")
	admin := securityAdminMySQL(t)
	otherName := "scope_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedOther, err := database.QuoteIdentifier("mysql", otherName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec("CREATE DATABASE " + quotedOther); err != nil {
		t.Fatal("cannot prepare second isolated test database")
	}
	t.Cleanup(func() { _, _ = admin.Exec("DROP DATABASE " + quotedOther) })
	if _, err := admin.Exec("CREATE TABLE " + quotedOther + ".victim(id INT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}

	unsafe := *f.connection
	unsafe.Username = os.Getenv("TARGET_MYSQL_ADMIN_USER")
	if unsafe.Username == "" {
		unsafe.Username = "root"
	}
	password := os.Getenv("TARGET_MYSQL_ADMIN_PASSWORD")
	if password == "" {
		password = os.Getenv("TARGET_MYSQL_PASSWORD")
	}
	if err := f.store.Update(context.Background(), unsafe.ID, unsafe, password); err != nil {
		t.Fatal(err)
	}
	f.manager.Invalidate(unsafe.ID)
	other := unsafe
	other.Name, other.DBName = "Second database without grant", otherName
	savedOther, err := f.store.Create(context.Background(), other, password)
	if err != nil {
		t.Fatal(err)
	}
	if response := f.request("GET", "/databases/"+savedOther.ID+"/migration", nil, false); response.Code != 403 {
		t.Fatalf("second database editor must require its own grant: HTTP %d", response.Code)
	}
	if response := f.request("GET", f.base, nil, false); response.Code != 200 {
		t.Fatalf("first database editor must remain accessible before execution: HTTP %d", response.Code)
	}

	f.request("POST", f.base+"/execute", url.Values{"sql_query": {"INSERT INTO " + quotedOther + ".victim VALUES(1)"}}, true)
	var count int
	if err := admin.QueryRow("SELECT COUNT(*) FROM " + quotedOther + ".victim").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("database without grant changed through another database URL: %d rows", count)
	}
	history, err := ListHistory(context.Background(), f.db, f.connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range history {
		if attempt.Status == Success {
			t.Error("unsafe account must be rejected before user SQL, not audited as successful")
		}
	}
}

func TestScopedMySQLBoundaryIsRecheckedForCachedPools(t *testing.T) {
	f := migrationFixture(t, "mysql")
	admin := securityAdminMySQL(t)
	other := "scope" + strings.ReplaceAll(uuid.NewString(), "-", "")
	qother, _ := database.QuoteIdentifier("mysql", other)
	if _, err := admin.Exec("CREATE DATABASE " + qother); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec("DROP DATABASE " + qother) })
	if _, err := admin.Exec("CREATE TABLE " + qother + ".victim(id int)"); err != nil {
		t.Fatal(err)
	}
	a := f.execute(t, "INSERT INTO "+qother+".victim VALUES(1)")
	if a.Status != Failed {
		t.Fatal("scoped credentials allowed access to another database")
	}
	var count int
	if err := admin.QueryRow("SELECT COUNT(*) FROM " + qother + ".victim").Scan(&count); err != nil || count != 0 {
		t.Fatalf("unauthorized data changed: %d %v", count, err)
	}
	if a := f.execute(t, "SELECT 1"); a.Status != Success || a.Version != 1 {
		t.Fatal("safe SQL did not execute with scoped account")
	}
	account := "'" + strings.ReplaceAll(f.connection.Username, "'", "''") + "'@'%'"
	if _, err := admin.Exec("GRANT SELECT ON " + qother + ".* TO " + account); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec("REVOKE SELECT ON " + qother + ".* FROM " + account) })
	if a := f.execute(t, "SELECT 2"); a.Status != Failed || !strings.Contains(a.ErrorMessage, "Akun MySQL") {
		t.Fatal("cached connection ignored newly broadened privileges")
	}
	if _, err := admin.Exec("REVOKE SELECT ON " + qother + ".* FROM " + account); err != nil {
		t.Fatal(err)
	}
	if a := f.execute(t, "SELECT 3"); a.Status != Success || a.Version != 2 {
		t.Fatal("restored scoped account could not continue candidate version")
	}
}

func TestMySQLRejectsElevatedDefinerObjects(t *testing.T) {
	for _, kind := range []string{"view", "trigger"} {
		t.Run(kind, func(t *testing.T) {
			f := migrationFixture(t, "mysql")
			admin := securityAdminMySQL(t)
			other := "definer" + strings.ReplaceAll(uuid.NewString(), "-", "")
			qother, _ := database.QuoteIdentifier("mysql", other)
			qdb, _ := database.QuoteIdentifier("mysql", f.connection.DBName)
			if _, err := admin.Exec("CREATE DATABASE " + qother); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = admin.Exec("DROP DATABASE " + qother) })
			if _, err := admin.Exec("CREATE TABLE " + qother + ".victim(id int)"); err != nil {
				t.Fatal(err)
			}
			var query string
			if kind == "view" {
				view := f.table + "_bridge"
				if _, err := admin.Exec("CREATE SQL SECURITY DEFINER VIEW " + qdb + "." + view + " AS SELECT id FROM " + qother + ".victim"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _, _ = admin.Exec("DROP VIEW " + qdb + "." + view) })
				query = "INSERT INTO " + view + " VALUES(1)"
			} else {
				if _, err := admin.Exec("CREATE TABLE " + qdb + "." + f.table + "(id int)"); err != nil {
					t.Fatal(err)
				}
				if _, err := admin.Exec("CREATE TRIGGER " + qdb + "." + f.table + "_trigger AFTER INSERT ON " + qdb + "." + f.table + " FOR EACH ROW INSERT INTO " + qother + ".victim VALUES(NEW.id)"); err != nil {
					t.Fatal(err)
				}
				query = "INSERT INTO "+f.table+" VALUES(1)"
			}
			a := f.execute(t, query)
			var count int
			if err := admin.QueryRow("SELECT COUNT(*) FROM " + qother + ".victim").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 || a.Status == Success {
				t.Fatalf("elevated %s bypassed database scope: rows=%d status=%s", kind, count, a.Status)
			}
		})
	}
}

func TestPostgresMigrationDoesNotLeakSessionState(t *testing.T) {
	f := migrationFixture(t, "postgres")
	f.target.SetMaxOpenConns(1)
	schema := f.table + "_session"
	if _, err := f.target.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.target.Exec("RESET ALL")
		_, _ = f.target.Exec("DROP SCHEMA " + schema + " CASCADE")
	})
	attempt, err := f.runner.Execute(context.Background(), f.connection.ID, f.user, "SELECT set_config('search_path', '"+schema+"', false)", "")
	if errors.Is(err, ErrInvalidSQL) {
		return // Rejecting session control before execution is also safe.
	}
	if err != nil || attempt.Status != Success {
		t.Fatalf("setting migration: %+v %v", attempt, err)
	}
	attempt = f.execute(t, "CREATE TABLE "+f.table+"(id int)")
	if attempt.Status != Success {
		t.Fatalf("later migration: %+v", attempt)
	}
	var actualSchema string
	if err := f.target.QueryRow("SELECT table_schema FROM information_schema.tables WHERE table_name=$1", f.table).Scan(&actualSchema); err != nil {
		t.Fatal(err)
	}
	if actualSchema != "public" {
		t.Errorf("later migration inherited previous session state: schema=%s, want public", actualSchema)
	}
}

func TestReconcileReceiptMustMatchEveryIdentity(t *testing.T) {
	for _, mismatch := range []string{"hash", "version", "database", "attempt", "none"} {
		t.Run(mismatch, func(t *testing.T) {
			f := migrationFixture(t, "postgres")
			attempt := f.execute(t, "SELECT 1")
			if _, err := f.db.Exec("UPDATE migrations SET status='PENDING',finished_at=NULL WHERE id=$1", attempt.ID); err != nil {
				t.Fatal(err)
			}
			var query string
			var value any
			switch mismatch {
			case "hash":
				query, value = "UPDATE public._dbplatform_migration_receipts SET sql_hash=$1 WHERE attempt_id=$2", strings.Repeat("0", 64)
			case "version":
				query, value = "UPDATE public._dbplatform_migration_receipts SET version=$1 WHERE attempt_id=$2", attempt.Version+1
			case "database":
				query, value = "UPDATE public._dbplatform_migration_receipts SET database_id=$1 WHERE attempt_id=$2", uuid.NewString()
			case "attempt":
				query, value = "UPDATE public._dbplatform_migration_receipts SET attempt_id=$1 WHERE attempt_id=$2", uuid.NewString()
			}
			if query != "" {
				if _, err := f.target.Exec(query, value, attempt.ID); err != nil {
					t.Fatal(err)
				}
			}
			reconciled, err := f.runner.Reconcile(context.Background(), f.connection.ID, attempt.ID)
			if mismatch == "none" {
				if err != nil || reconciled.Status != Success {
					t.Fatalf("matching receipt must reconcile: %+v %v", reconciled, err)
				}
				return
			}
			if !errors.Is(err, ErrPending) || reconciled.Status != Pending {
				t.Fatalf("mismatched %s must remain PENDING: %+v %v", mismatch, reconciled, err)
			}
			persisted, err := GetAttempt(context.Background(), f.db, f.connection.ID, attempt.ID)
			if err != nil || persisted.Status != Pending {
				t.Fatalf("mismatch changed persisted status: %+v %v", persisted, err)
			}
			if _, err := f.runner.Execute(context.Background(), f.connection.ID, f.user, "SELECT 2", ""); !errors.Is(err, ErrPending) {
				t.Fatalf("unresolved evidence must continue blocking execution: %v", err)
			}
		})
	}
}

func TestMySQLPartialFailureWithoutReceiptRemainsPending(t *testing.T) {
	f := migrationFixture(t, "mysql")
	if _, err := f.db.Exec(`CREATE FUNCTION prevent_partial_finalize() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'platform unavailable'; END $$;
	CREATE TRIGGER prevent_partial_finalize BEFORE UPDATE ON migrations FOR EACH ROW EXECUTE FUNCTION prevent_partial_finalize()`); err != nil {
		t.Fatal(err)
	}
	attempt, err := f.runner.Execute(context.Background(), f.connection.ID, f.user,
		"CREATE TABLE "+f.table+"(id int) ENGINE=InnoDB; INSERT INTO "+f.table+" VALUES(1); INSERT INTO missing_security_test_table VALUES(1)", "")
	if !errors.Is(err, ErrPending) || attempt == nil || attempt.Status != Pending {
		t.Fatalf("failed platform finalization must leave PENDING: %+v %v", attempt, err)
	}
	var rows int
	if err := f.target.QueryRow("SELECT COUNT(*) FROM " + f.table).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("fixture must retain partial DDL and roll back DML: rows=%d err=%v", rows, err)
	}
	if _, err := f.db.Exec("DROP TRIGGER prevent_partial_finalize ON migrations"); err != nil {
		t.Fatal(err)
	}
	reconciled, err := f.runner.Reconcile(context.Background(), f.connection.ID, attempt.ID)
	if !errors.Is(err, ErrPending) || reconciled.Status != Pending {
		t.Fatalf("partial MySQL changes must never count as success: %+v %v", reconciled, err)
	}
}
