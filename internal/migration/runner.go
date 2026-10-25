package migration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
)

const MySQLWarning = "Peringatan (MySQL): Perintah DDL dapat melakukan implicit commit. Kegagalan di tengah eksekusi DDL multi-statement tidak dapat di-rollback otomatis secara penuh."

type Runner struct {
	DB *sql.DB
	// Locks uses a separate platform pool so row locks cannot exhaust audit IO.
	Locks   *sql.DB
	Manager *database.TargetManager
}

// Execute never retries uncertain work. A retry creates a new audit attempt and
// is permitted only for FAILED at the next successful version. Callers enforce
// VIEW_EDIT and CSRF before invoking it, including generated SQL.
func (r *Runner) Execute(ctx context.Context, databaseID, userID, query, retryID string) (*Attempt, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if r.Locks == nil {
		return nil, ErrHistory
	}
	lock, err := lockDatabase(ctx, r.Locks, databaseID)
	if err != nil {
		return nil, err
	}
	defer lock.Rollback()
	var pending bool
	if err := lock.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM migrations WHERE database_id=$1 AND status='PENDING')", databaseID).Scan(&pending); err != nil {
		return nil, ErrHistory
	}
	if pending {
		return nil, ErrPending
	}
	var version int
	if err := lock.QueryRowContext(ctx, "SELECT COALESCE(MAX(version),0)+1 FROM migrations WHERE database_id=$1 AND status='SUCCESS'", databaseID).Scan(&version); err != nil {
		return nil, ErrHistory
	}
	if retryID != "" {
		old, err := GetAttempt(ctx, r.DB, databaseID, retryID)
		if err != nil {
			return nil, err
		}
		if old.Status != Failed || old.Version != version {
			return nil, ErrStaleRetry
		}
		query = old.SQL
	}
	var dbms string
	if err := lock.QueryRowContext(ctx, "SELECT dbms_type FROM database_connections WHERE id=$1", databaseID).Scan(&dbms); err != nil {
		return nil, ErrHistory
	}
	statements, err := ValidateSQL(dbms, query)
	if err != nil {
		return nil, err
	}
	a := &Attempt{ID: uuid.NewString(), DatabaseID: databaseID, Version: version, SQL: query, Status: Pending}
	// Independent autocommit persists the attempt even if this process dies while
	// the platform lock transaction is open or the target commit is in flight.
	err = r.DB.QueryRowContext(ctx, `INSERT INTO migrations(id,database_id,version,sql_query,executed_by_user_id,status)
	 VALUES($1,$2,$3,$4,$5,'PENDING') RETURNING started_at`, a.ID, databaseID, version, query, userID).Scan(&a.StartedAt)
	if err != nil {
		return nil, ErrHistory
	}
	status, message, partial := r.runTarget(ctx, a, statements, dbms)
	if status == Pending {
		return a, ErrPending
	}
	// Request cancellation must not prevent a known result from being recorded.
	finalCtx, finalCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer finalCancel()
	if err := finish(finalCtx, r.DB, a, status, message, partial, time.Now().UTC()); err != nil {
		return a, err
	}
	return a, nil
}

func sqlHash(query string) string { h := sha256.Sum256([]byte(query)); return hex.EncodeToString(h[:]) }

func receiptTable(dbms string) string {
	if dbms == "postgres" {
		return `public._dbplatform_migration_receipts`
	}
	return "`_dbplatform_migration_receipts`"
}

func (r *Runner) runTarget(ctx context.Context, a *Attempt, statements []Statement, dbms string) (Status, string, bool) {
	target, actual, err := r.Manager.Open(ctx, a.DatabaseID)
	if err != nil || actual != dbms {
		return Failed, "Database target tidak dapat diakses atau konfigurasinya berubah.", false
	}
	if dbms == "mysql" {
		var name string
		if err := target.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&name); err != nil {
			return Failed, "Database target tidak dapat diakses.", false
		}
		if err := database.ValidateMySQLScope(ctx, target, name); err != nil {
			return Failed, err.Error(), false
		}
	}
	ddl := "CREATE TABLE IF NOT EXISTS " + receiptTable(dbms) + ` (
	 attempt_id VARCHAR(36) PRIMARY KEY, database_id VARCHAR(36) NOT NULL,
	 version INT NOT NULL, sql_hash VARCHAR(64) NOT NULL, completed_unix_ms BIGINT NOT NULL)`
	if dbms == "mysql" {
		ddl += " ENGINE=InnoDB"
	}
	// Pin one physical session across MySQL's DDL transaction boundaries.
	conn, err := target.Conn(ctx)
	if err != nil {
		return Failed, "Koneksi target tidak dapat dimulai.", false
	}
	// Arbitrary SQL can change session state through functions (not just SET).
	// Never lend that physical session to another migration or viewer request.
	defer func() {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		_ = conn.Close()
	}()
	if _, err := conn.ExecContext(ctx, ddl); err != nil {
		return Failed, "Tabel bukti migration tidak dapat disiapkan. Periksa izin CREATE pada target.", false
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return Failed, "Transaksi target tidak dapat dimulai.", false
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	if dbms == "postgres" {
		if _, err := tx.ExecContext(ctx, "SET LOCAL statement_timeout = '8s'"); err != nil {
			return Failed, executionError(err), false
		}
	}
	partial := false
	for _, statement := range statements {
		// Preparing also asks the server to enforce the single-statement boundary.
		prepared, err := tx.PrepareContext(ctx, statement.SQL)
		if err == nil {
			_, err = prepared.ExecContext(ctx)
			_ = prepared.Close()
		}
		if err != nil {
			rollbackErr := tx.Rollback()
			if rollbackErr != nil {
				return Pending, "", false
			}
			return Failed, executionError(err), dbms == "mysql"
		}
		if dbms == "mysql" && isDDL(statement.Kind) {
			partial = true
			// DDL ends MySQL's server-side transaction. Begin a fresh one so DML
			// following that DDL is not silently executed under autocommit.
			if err := tx.Commit(); err != nil {
				return Pending, "", false
			}
			tx, err = conn.BeginTx(ctx, nil)
			if err != nil {
				return Failed, "DDL MySQL telah diterapkan, tetapi transaksi lanjutan gagal dimulai.", true
			}
		}
	}
	insert := "INSERT INTO " + receiptTable(dbms) + "(attempt_id,database_id,version,sql_hash,completed_unix_ms) VALUES($1,$2,$3,$4,$5)"
	if dbms == "mysql" {
		insert = "INSERT INTO " + receiptTable(dbms) + "(attempt_id,database_id,version,sql_hash,completed_unix_ms) VALUES(?,?,?,?,?)"
	}
	if _, err := tx.ExecContext(ctx, insert, a.ID, a.DatabaseID, a.Version, sqlHash(a.SQL), time.Now().UTC().UnixMilli()); err != nil {
		if tx.Rollback() != nil {
			return Pending, "", false
		}
		return Failed, "Bukti hasil migration tidak dapat disimpan pada target.", partial
	}
	if err := tx.Commit(); err != nil {
		// A lost acknowledgement does not prove rollback. The receipt, committed
		// with the final transaction, allows a later proof-based reconciliation.
		return Pending, "", false
	}
	return Success, "", false
}

func isDDL(kind string) bool {
	switch kind {
	case "CREATE", "ALTER", "DROP", "TRUNCATE", "COMMENT", "GRANT", "REVOKE":
		return true
	}
	return false
}

// Reconcile needs the durable receipt for this exact attempt, database, version
// and SQL hash. Missing objects/receipts NEVER establish failure or success.
// MySQL writes its receipt only after every statement has completed.
func (r *Runner) Reconcile(ctx context.Context, databaseID, id string) (*Attempt, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if r.Locks == nil {
		return nil, ErrHistory
	}
	lock, err := lockDatabase(ctx, r.Locks, databaseID)
	if err != nil {
		return nil, err
	}
	defer lock.Rollback()
	a, err := GetAttempt(ctx, r.DB, databaseID, id)
	if err != nil {
		return nil, err
	}
	if a.Status != Pending {
		return a, nil
	}
	target, dbms, err := r.Manager.Open(ctx, databaseID)
	if err != nil {
		return a, ErrPending
	}
	query := "SELECT version,sql_hash,completed_unix_ms FROM " + receiptTable(dbms) + " WHERE attempt_id=$1 AND database_id=$2"
	if dbms == "mysql" {
		query = "SELECT version,sql_hash,completed_unix_ms FROM " + receiptTable(dbms) + " WHERE attempt_id=? AND database_id=?"
	}
	var version int
	var hash string
	var completed int64
	err = target.QueryRowContext(ctx, query, id, databaseID).Scan(&version, &hash, &completed)
	if err != nil || version != a.Version || hash != sqlHash(a.SQL) {
		return a, ErrPending
	}
	if err := finish(ctx, r.DB, a, Success, "", false, time.UnixMilli(completed).UTC()); err != nil {
		return a, err
	}
	return a, nil
}

// IsConflict reports whether a form should show a resolvable execution conflict.
func IsConflict(err error) bool {
	return errors.Is(err, ErrBusy) || errors.Is(err, ErrPending) || errors.Is(err, ErrStaleRetry)
}
