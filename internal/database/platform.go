package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/config"
)

const migrationCoreSQL = `
ALTER TABLE migrations ADD COLUMN IF NOT EXISTS started_at TIMESTAMPTZ;
ALTER TABLE migrations ADD COLUMN IF NOT EXISTS finished_at TIMESTAMPTZ;
ALTER TABLE migrations ADD COLUMN IF NOT EXISTS partial_changes BOOLEAN NOT NULL DEFAULT FALSE;
UPDATE migrations SET started_at=executed_at WHERE started_at IS NULL;
ALTER TABLE migrations ALTER COLUMN started_at SET DEFAULT CURRENT_TIMESTAMP;
ALTER TABLE migrations ALTER COLUMN started_at SET NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_migrations_success_version
 ON migrations(database_id, version) WHERE status='SUCCESS';
`

const sessionGraceTokensSQL = `
CREATE TABLE IF NOT EXISTS session_grace_tokens (
    token VARCHAR(128) PRIMARY KEY,
    session_id VARCHAR(128) NOT NULL REFERENCES sessions(id) ON UPDATE CASCADE ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_session_grace_tokens_session ON session_grace_tokens(session_id);
INSERT INTO session_grace_tokens(token, session_id, expires_at)
SELECT previous_id, id, grace_until FROM sessions
WHERE previous_id IS NOT NULL AND grace_until > CURRENT_TIMESTAMP
ON CONFLICT (token) DO UPDATE SET expires_at=LEAST(session_grace_tokens.expires_at, EXCLUDED.expires_at);
`

const defaultInitPlatformSQL = `
CREATE TABLE IF NOT EXISTS users (
    id VARCHAR(36) PRIMARY KEY,
    username VARCHAR(100) UNIQUE NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    is_platform_admin BOOLEAN NOT NULL DEFAULT FALSE,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);

CREATE TABLE IF NOT EXISTS sessions (
    id VARCHAR(128) PRIMARY KEY,
    user_id VARCHAR(36) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_active_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMPTZ NOT NULL,
    previous_id VARCHAR(128),
    grace_until TIMESTAMPTZ,
    csrf_token VARCHAR(128) NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_previous_id ON sessions(previous_id);
CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);

CREATE TABLE IF NOT EXISTS groups (
    id VARCHAR(36) PRIMARY KEY,
    name VARCHAR(100) UNIQUE NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS user_groups (
    user_id VARCHAR(36) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id VARCHAR(36) NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, group_id)
);

CREATE INDEX IF NOT EXISTS idx_user_groups_user ON user_groups(user_id);
CREATE INDEX IF NOT EXISTS idx_user_groups_group ON user_groups(group_id);

CREATE TABLE IF NOT EXISTS database_connections (
    id VARCHAR(36) PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    dbms_type VARCHAR(20) NOT NULL,
    host VARCHAR(255) NOT NULL,
    port INT NOT NULL,
    db_name VARCHAR(100) NOT NULL,
    username VARCHAR(100) NOT NULL,
    password_encrypted TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS group_database_grants (
    group_id VARCHAR(36) NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    database_id VARCHAR(36) NOT NULL REFERENCES database_connections(id) ON DELETE CASCADE,
    access_level VARCHAR(20) NOT NULL,
    PRIMARY KEY (group_id, database_id)
);

CREATE INDEX IF NOT EXISTS idx_grants_group ON group_database_grants(group_id);
CREATE INDEX IF NOT EXISTS idx_grants_database ON group_database_grants(database_id);

CREATE TABLE IF NOT EXISTS migrations (
    id VARCHAR(36) PRIMARY KEY,
    database_id VARCHAR(36) NOT NULL REFERENCES database_connections(id) ON DELETE CASCADE,
    version INT NOT NULL,
    sql_query TEXT NOT NULL,
    executed_by_user_id VARCHAR(36) REFERENCES users(id) ON DELETE SET NULL,
    executed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    status VARCHAR(20) NOT NULL,
    error_message TEXT
);

CREATE INDEX IF NOT EXISTS idx_migrations_database_version ON migrations(database_id, version);
`

// NewPlatformDB initializes and verifies the PostgreSQL connection pool for the platform.
func NewPlatformDB(cfg *config.Config) (*sql.DB, error) {
	dsn := cfg.PlatformDB.DSN()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database connection: %w", err)
	}

	// Configure pool parameters
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(15 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to ping platform database at %s:%d: %w", cfg.PlatformDB.Host, cfg.PlatformDB.Port, err)
	}

	return db, nil
}

// MigratePlatformDB applies the initial schema and additive upgrades idempotently.
func MigratePlatformDB(ctx context.Context, db *sql.DB) error {
	sqlContent := defaultInitPlatformSQL

	// Check if migration file on disk exists and can be read
	candidates := []string{
		"migrations/platform/001_init_platform.sql",
		"../../migrations/platform/001_init_platform.sql",
		"../migrations/platform/001_init_platform.sql",
	}
	for _, path := range candidates {
		if content, err := os.ReadFile(filepath.Clean(path)); err == nil && len(content) > 0 {
			sqlContent = string(content)
			break
		}
	}

	_, err := db.ExecContext(ctx, sqlContent)
	if err != nil {
		return fmt.Errorf("failed to execute platform database migration: %w", err)
	}
	if _, err := db.ExecContext(ctx, migrationCoreSQL); err != nil {
		return fmt.Errorf("failed to upgrade migration metadata: %w", err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE sessions ADD COLUMN IF NOT EXISTS last_rotated_at TIMESTAMPTZ;
UPDATE sessions SET last_rotated_at=created_at WHERE last_rotated_at IS NULL;
ALTER TABLE sessions ALTER COLUMN last_rotated_at SET DEFAULT CURRENT_TIMESTAMP;
ALTER TABLE sessions ALTER COLUMN last_rotated_at SET NOT NULL;`); err != nil {
		return fmt.Errorf("failed to upgrade session rotation clock: %w", err)
	}
	if _, err := db.ExecContext(ctx, sessionGraceTokensSQL); err != nil {
		return fmt.Errorf("failed to upgrade session grace tokens: %w", err)
	}

	return nil
}
