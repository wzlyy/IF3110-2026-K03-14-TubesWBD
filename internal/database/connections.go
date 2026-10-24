package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Connection is safe to render: it never contains credentials.
type Connection struct {
	ID, Name, DBMS, Host string
	Port                 int
	DBName, Username     string
	CreatedAt, UpdatedAt time.Time
}

var ErrConnectionNotFound = errors.New("connection not found")

// ErrInvalidConnection identifies invalid form input, distinct from storage failures.
var ErrInvalidConnection = errors.New("invalid connection")

// ConnectionStore persists target metadata and AES-256-GCM encrypted passwords.
type ConnectionStore struct {
	db     *sql.DB
	cipher *credentialCipher
}

func NewConnectionStore(db *sql.DB, key string) (*ConnectionStore, error) {
	c, err := newCredentialCipher(key)
	if err != nil {
		return nil, err
	}
	return &ConnectionStore{db: db, cipher: c}, nil
}

func validateConnection(c Connection, password string) (Connection, error) {
	c.Name = strings.TrimSpace(c.Name)
	c.Host = strings.TrimSpace(c.Host)
	c.DBName = strings.TrimSpace(c.DBName)
	c.Username = strings.TrimSpace(c.Username)
	for _, f := range []struct {
		name, value string
		max         int
	}{
		{"Name", c.Name, 100}, {"Host", c.Host, 255}, {"Database name", c.DBName, 100}, {"Username", c.Username, 100},
	} {
		if f.value == "" || utf8.RuneCountInString(f.value) > f.max || strings.ContainsFunc(f.value, unicode.IsControl) {
			return c, fmt.Errorf("%w: %s is required and must be at most %d characters without control characters", ErrInvalidConnection, f.name, f.max)
		}
	}
	if c.DBMS != "postgres" && c.DBMS != "mysql" {
		return c, fmt.Errorf("%w: select a supported database type: PostgreSQL or MySQL", ErrInvalidConnection)
	}
	if c.Port < 1 || c.Port > 65535 {
		return c, fmt.Errorf("%w: port must be between 1 and 65535", ErrInvalidConnection)
	}
	if strings.ContainsAny(c.Host, "/\\?#@ ") || (strings.Contains(c.Host, ":") && net.ParseIP(c.Host) == nil) {
		return c, fmt.Errorf("%w: host must be a hostname or IP address without a port", ErrInvalidConnection)
	}
	if len(password) > 4096 || strings.ContainsRune(password, 0) {
		return c, fmt.Errorf("%w: password must be at most 4096 bytes without null characters", ErrInvalidConnection)
	}
	return c, nil
}

const connectionColumns = "id, name, dbms_type, host, port, db_name, username, created_at, updated_at"

type connectionScanner interface{ Scan(...any) error }

func scanConnection(row connectionScanner) (*Connection, error) {
	var c Connection
	err := row.Scan(&c.ID, &c.Name, &c.DBMS, &c.Host, &c.Port, &c.DBName, &c.Username, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrConnectionNotFound
	}
	if err != nil {
		return nil, errors.New("cannot load saved connection")
	}
	return &c, nil
}

func (s *ConnectionStore) List(ctx context.Context) ([]Connection, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+connectionColumns+" FROM database_connections ORDER BY name, id")
	if err != nil {
		return nil, errors.New("cannot load saved connections")
	}
	defer rows.Close()
	var connections []Connection
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		connections = append(connections, *c)
	}
	if rows.Err() != nil {
		return nil, errors.New("cannot load saved connections")
	}
	return connections, nil
}

func (s *ConnectionStore) Get(ctx context.Context, id string) (*Connection, error) {
	return scanConnection(s.db.QueryRowContext(ctx, "SELECT "+connectionColumns+" FROM database_connections WHERE id=$1", id))
}

func (s *ConnectionStore) Create(ctx context.Context, c Connection, password string) (*Connection, error) {
	c, err := validateConnection(c, password)
	if err != nil {
		return nil, err
	}
	encrypted, err := s.cipher.encrypt(password)
	if err != nil {
		return nil, err
	}
	c.ID = uuid.NewString()
	return scanConnection(s.db.QueryRowContext(ctx, `INSERT INTO database_connections
		(id, name, dbms_type, host, port, db_name, username, password_encrypted)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+connectionColumns,
		c.ID, c.Name, c.DBMS, c.Host, c.Port, c.DBName, c.Username, encrypted))
}

// Update preserves the existing encrypted password when password is empty.
// Call TargetManager.Invalidate after a successful update before returning to a caller.
func (s *ConnectionStore) Update(ctx context.Context, id string, c Connection, password string) error {
	c, err := validateConnection(c, password)
	if err != nil {
		return err
	}
	var encrypted string
	if password != "" {
		encrypted, err = s.cipher.encrypt(password)
		if err != nil {
			return err
		}
	}
	result, err := s.db.ExecContext(ctx, `UPDATE database_connections SET
		name=$2, dbms_type=$3, host=$4, port=$5, db_name=$6, username=$7,
		password_encrypted=CASE WHEN $8='' THEN password_encrypted ELSE $8 END,
		updated_at=CURRENT_TIMESTAMP WHERE id=$1`, id, c.Name, c.DBMS, c.Host, c.Port, c.DBName, c.Username, encrypted)
	return connectionWriteResult(result, err)
}

// Delete cascades to connection grants and migration history.
// Call TargetManager.Invalidate after successful deletion.
func (s *ConnectionStore) Delete(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("cannot save connection changes")
	}
	defer tx.Rollback()
	var found string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM database_connections WHERE id=$1 FOR UPDATE", id).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConnectionNotFound
		}
		return errors.New("cannot save connection changes")
	}
	// Capture affected users before cascading grants. Session failure must leave
	// the connection, its grants and its history intact.
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id IN (
	 SELECT ug.user_id FROM user_groups ug JOIN group_database_grants gdg ON gdg.group_id=ug.group_id
	 WHERE gdg.database_id=$1)`, id); err != nil {
		return errors.New("cannot save connection changes")
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM database_connections WHERE id=$1", id)
	if err := connectionWriteResult(result, err); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot save connection changes")
	}
	return nil
}

func connectionWriteResult(result sql.Result, err error) error {
	if err != nil {
		return errors.New("cannot save connection changes")
	}
	n, err := result.RowsAffected()
	if err != nil {
		return errors.New("cannot save connection changes")
	}
	if n == 0 {
		return ErrConnectionNotFound
	}
	return nil
}
