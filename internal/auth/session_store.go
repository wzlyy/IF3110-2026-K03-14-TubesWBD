package auth

import (
	"context"
	"database/sql"
)

// SessionStore allows permission mutations and session rotation/revocation to
// share one platform transaction. Both *sql.DB and *sql.Tx implement it.
type SessionStore interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
