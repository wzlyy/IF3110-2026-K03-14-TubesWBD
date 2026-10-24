package migration

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

type Status string

const (
	Pending Status = "PENDING"
	Success Status = "SUCCESS"
	Failed  Status = "FAILED"
)

var (
	ErrBusy       = errors.New("Migration lain sedang berjalan pada database ini. Tunggu hingga selesai.")
	ErrPending    = errors.New("Hasil migration belum dapat dipastikan. Jalankan rekonsiliasi; SQL tidak akan dijalankan ulang otomatis. Pemeriksaan lanjutan diperlukan jika bukti belum cukup.")
	ErrNotFound   = errors.New("Migration tidak ditemukan pada database ini.")
	ErrStaleRetry = errors.New("Retry hanya tersedia untuk percobaan FAILED pada versi kandidat saat ini. Tinjau perubahan parsial sebelum mencoba kembali.")
	ErrHistory    = errors.New("Riwayat migration tidak dapat dibaca atau disimpan. Coba kembali.")
)

type Attempt struct {
	ID, DatabaseID, SQL, Executor, ErrorMessage string
	Version                                     int
	Status                                      Status
	StartedAt                                   time.Time
	FinishedAt                                  *time.Time
	PartialChanges                              bool
}

func (a Attempt) StartedUTC() string { return a.StartedAt.UTC().Format("2006-01-02 15:04:05 UTC") }
func (a Attempt) FinishedUTC() string {
	if a.FinishedAt == nil {
		return "Belum tercatat"
	}
	return a.FinishedAt.UTC().Format("2006-01-02 15:04:05 UTC")
}
func (a Attempt) Snippet() string {
	text := []rune(strings.Join(strings.Fields(a.SQL), " "))
	if len(text) > 100 {
		return string(text[:100]) + "…"
	}
	return string(text)
}

const attemptColumns = `m.id,m.database_id,m.version,m.sql_query,COALESCE(u.username,'Pengguna dihapus'),m.status,COALESCE(m.error_message,''),m.started_at,m.finished_at,m.partial_changes`

func scanAttempt(row interface{ Scan(...any) error }) (*Attempt, error) {
	var a Attempt
	if err := row.Scan(&a.ID, &a.DatabaseID, &a.Version, &a.SQL, &a.Executor, &a.Status, &a.ErrorMessage, &a.StartedAt, &a.FinishedAt, &a.PartialChanges); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, ErrHistory
	}
	return &a, nil
}

func GetAttempt(ctx context.Context, db *sql.DB, databaseID, id string) (*Attempt, error) {
	return scanAttempt(db.QueryRowContext(ctx, "SELECT "+attemptColumns+" FROM migrations m LEFT JOIN users u ON u.id=m.executed_by_user_id WHERE m.database_id=$1 AND m.id=$2", databaseID, id))
}

func ListHistory(ctx context.Context, db *sql.DB, databaseID string) ([]Attempt, error) {
	rows, err := db.QueryContext(ctx, "SELECT "+attemptColumns+" FROM migrations m LEFT JOIN users u ON u.id=m.executed_by_user_id WHERE m.database_id=$1 ORDER BY m.started_at DESC,m.id DESC", databaseID)
	if err != nil {
		return nil, ErrHistory
	}
	defer rows.Close()
	var history []Attempt
	for rows.Next() {
		a, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		history = append(history, *a)
	}
	if rows.Err() != nil {
		return nil, ErrHistory
	}
	return history, nil
}

// lockDatabase holds a row lock for the ENTIRE execution/reconciliation. NO KEY
// UPDATE permits independent FK inserts, making PENDING durable before target IO.
// PostgreSQL coordinates different server processes, unlike an in-process mutex.
func lockDatabase(ctx context.Context, db *sql.DB, id string) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, ErrHistory
	}
	var found string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM database_connections WHERE id=$1 FOR NO KEY UPDATE NOWAIT", id).Scan(&found); err != nil {
		_ = tx.Rollback()
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) && pgerr.Code == "55P03" {
			return nil, ErrBusy
		}
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, ErrHistory
	}
	return tx, nil
}

func finish(ctx context.Context, db *sql.DB, a *Attempt, status Status, message string, partial bool, completed time.Time) error {
	result, err := db.ExecContext(ctx, `UPDATE migrations SET status=$3,error_message=NULLIF($4,''),partial_changes=$5,finished_at=$6
	 WHERE database_id=$1 AND id=$2 AND status='PENDING'`, a.DatabaseID, a.ID, status, message, partial, completed)
	if err != nil {
		return ErrPending
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return ErrPending
	}
	a.Status, a.ErrorMessage, a.PartialChanges, a.FinishedAt = status, message, partial, &completed
	return nil
}
