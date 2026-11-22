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

const defaultM2SchemaSQL = `
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS leader_user_id VARCHAR(36) REFERENCES users(id) ON DELETE RESTRICT;
CREATE INDEX IF NOT EXISTS idx_groups_leader ON groups(leader_user_id);

CREATE TABLE IF NOT EXISTS migration_definitions (
    id                   VARCHAR(36) PRIMARY KEY,
    database_id          VARCHAR(36) NOT NULL REFERENCES database_connections(id) ON DELETE CASCADE,
    version              INT NOT NULL,
    revision             INT NOT NULL DEFAULT 1,
    name                 VARCHAR(200) NOT NULL,
    description          TEXT NOT NULL DEFAULT '',
    sql_up               TEXT NOT NULL,
    sql_down             TEXT,
    is_irreversible      BOOLEAN NOT NULL DEFAULT FALSE,
    irreversible_reason  TEXT,
    execution_mode       VARCHAR(16) NOT NULL DEFAULT 'normal'
                         CHECK (execution_mode IN ('normal', 'batch')),
    batch_table          VARCHAR(128),
    batch_cursor_column  VARCHAR(128),
    batch_size           INT CHECK (batch_size IS NULL OR batch_size > 0),
    is_immutable         BOOLEAN NOT NULL DEFAULT FALSE,
    created_by           VARCHAR(36) REFERENCES users(id) ON DELETE SET NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (database_id, version),
    CONSTRAINT chk_reversible_rule CHECK (
        (is_irreversible = FALSE AND sql_down IS NOT NULL AND length(trim(sql_down)) > 0)
        OR
        (is_irreversible = TRUE AND irreversible_reason IS NOT NULL AND length(trim(irreversible_reason)) > 0)
    ),
    CONSTRAINT chk_batch_config CHECK (
        execution_mode = 'normal'
        OR (
            batch_table IS NOT NULL AND length(trim(batch_table)) > 0 AND
            batch_cursor_column IS NOT NULL AND length(trim(batch_cursor_column)) > 0 AND
            batch_size IS NOT NULL AND batch_size > 0
        )
    )
);
CREATE INDEX IF NOT EXISTS idx_mig_def_db_version ON migration_definitions(database_id, version ASC);

CREATE TABLE IF NOT EXISTS migration_requests (
    id                   VARCHAR(36) PRIMARY KEY,
    database_id          VARCHAR(36) NOT NULL REFERENCES database_connections(id) ON DELETE CASCADE,
    migration_id         VARCHAR(36) NOT NULL REFERENCES migration_definitions(id) ON DELETE CASCADE,
    action_type          VARCHAR(24) NOT NULL
                         CHECK (action_type IN ('up', 'rollback', 'forward', 'resume_batch', 'revert_batch')),
    target_execution_id  VARCHAR(36),
    status               VARCHAR(32) NOT NULL DEFAULT 'draft'
                         CHECK (status IN (
                             'draft', 'pending_review', 'approved', 'rejected',
                             'pending_queue', 'running', 'success', 'failed',
                             'partial', 'partially_reverted', 'uncertain'
                         )),
    requested_by         VARCHAR(36) REFERENCES users(id) ON DELETE SET NULL,
    batch_size_override  INT CHECK (batch_size_override IS NULL OR batch_size_override > 0),
    job_id               VARCHAR(36),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    submitted_at         TIMESTAMPTZ,
    approved_at          TIMESTAMPTZ,
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_mig_req_db_status ON migration_requests(database_id, status);
CREATE INDEX IF NOT EXISTS idx_mig_req_job_id ON migration_requests(job_id);

CREATE TABLE IF NOT EXISTS migration_approvals (
    id                    VARCHAR(36) PRIMARY KEY,
    request_id            VARCHAR(36) NOT NULL REFERENCES migration_requests(id) ON DELETE CASCADE,
    group_id              VARCHAR(36) NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    leader_user_id        VARCHAR(36) NOT NULL REFERENCES users(id),
    required_access_level VARCHAR(20) NOT NULL CHECK (required_access_level IN ('VIEW_ONLY', 'VIEW_EDIT')),
    decision              VARCHAR(16) NOT NULL DEFAULT 'pending'
                          CHECK (decision IN ('pending', 'approved', 'rejected')),
    comment               TEXT NOT NULL DEFAULT '',
    decided_at            TIMESTAMPTZ,
    UNIQUE (request_id, group_id)
);
CREATE INDEX IF NOT EXISTS idx_mig_app_leader ON migration_approvals(leader_user_id, decision);

CREATE TABLE IF NOT EXISTS migration_executions (
    execution_id            VARCHAR(36) PRIMARY KEY,
    request_id              VARCHAR(36) NOT NULL REFERENCES migration_requests(id) ON DELETE CASCADE,
    job_id                  VARCHAR(36) NOT NULL,
    database_id             VARCHAR(36) NOT NULL REFERENCES database_connections(id) ON DELETE CASCADE,
    migration_id            VARCHAR(36) NOT NULL REFERENCES migration_definitions(id) ON DELETE CASCADE,
    migration_version       INT NOT NULL,
    migration_revision      INT NOT NULL,
    migration_name          VARCHAR(200) NOT NULL,
    action_type             VARCHAR(24) NOT NULL
                            CHECK (action_type IN ('up', 'rollback', 'forward', 'resume_batch', 'revert_batch')),
    reverted_execution_id   VARCHAR(36) REFERENCES migration_executions(execution_id),
    parent_execution_id     VARCHAR(36) REFERENCES migration_executions(execution_id),
    attempt_number          INT NOT NULL DEFAULT 1,
    executed_by             VARCHAR(36) REFERENCES users(id) ON DELETE SET NULL,
    executed_sql_snapshot   TEXT NOT NULL,
    execution_mode          VARCHAR(16) NOT NULL CHECK (execution_mode IN ('normal', 'batch')),
    status                  VARCHAR(32) NOT NULL
                            CHECK (status IN ('running', 'success', 'failed', 'partial', 'partially_reverted', 'uncertain')),
    total_batches_committed INT NOT NULL DEFAULT 0,
    total_rows_affected     BIGINT NOT NULL DEFAULT 0,
    error_message           TEXT,
    error_sqlstate          VARCHAR(16),
    trace_id                VARCHAR(64),
    started_at              TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at             TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_exec_db_started ON migration_executions(database_id, started_at ASC, execution_id ASC);
CREATE INDEX IF NOT EXISTS idx_exec_reverted   ON migration_executions(reverted_execution_id);
CREATE INDEX IF NOT EXISTS idx_exec_job_id     ON migration_executions(job_id);

CREATE TABLE IF NOT EXISTS database_locks (
    database_id    VARCHAR(36) PRIMARY KEY REFERENCES database_connections(id) ON DELETE CASCADE,
    lock_token     VARCHAR(36) NOT NULL,
    worker_id      VARCHAR(128) NOT NULL,
    execution_id   VARCHAR(36) NOT NULL,
    hold_state     VARCHAR(24) NOT NULL CHECK (hold_state IN ('running', 'held_partial', 'held_uncertain')),
    acquired_at    TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    heartbeat_at   TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at     TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS execution_batch_progress (
    id                VARCHAR(36) PRIMARY KEY,
    execution_id      VARCHAR(36) NOT NULL REFERENCES migration_executions(execution_id) ON DELETE CASCADE,
    batch_number      INT NOT NULL,
    cursor_start      TEXT,
    cursor_end        TEXT,
    target_max_cursor TEXT,
    rows_affected     BIGINT NOT NULL DEFAULT 0,
    direction         VARCHAR(16) NOT NULL CHECK (direction IN ('forward', 'revert')),
    status            VARCHAR(16) NOT NULL CHECK (status IN ('committed', 'reverted', 'failed')),
    committed_at      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (execution_id, batch_number, direction)
);
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

	m2SQL := defaultM2SchemaSQL
	m2Candidates := []string{
		"migrations/platform/005_m2_schema.sql",
		"../../migrations/platform/005_m2_schema.sql",
		"../migrations/platform/005_m2_schema.sql",
	}
	for _, path := range m2Candidates {
		if content, err := os.ReadFile(filepath.Clean(path)); err == nil && len(content) > 0 {
			m2SQL = string(content)
			break
		}
	}
	if _, err := db.ExecContext(ctx, m2SQL); err != nil {
		return fmt.Errorf("failed to apply milestone 2 platform schema: %w", err)
	}

	return nil
}
