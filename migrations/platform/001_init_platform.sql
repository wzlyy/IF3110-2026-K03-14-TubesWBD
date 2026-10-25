-- ==============================================================================
-- Platform Database Initialization Script
-- ==============================================================================

-- 1. Table users
CREATE TABLE IF NOT EXISTS users (
    id VARCHAR(36) PRIMARY KEY,
    username VARCHAR(100) UNIQUE NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    is_platform_admin BOOLEAN NOT NULL DEFAULT FALSE,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Index on username for fast authentication lookup
CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);

-- 2. Table sessions
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

-- Index for session lookup by previous_id during grace period
CREATE INDEX IF NOT EXISTS idx_sessions_previous_id ON sessions(previous_id);
-- Index for quick lookup by user_id for revocation
CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);

-- 3. Table groups
CREATE TABLE IF NOT EXISTS groups (
    id VARCHAR(36) PRIMARY KEY,
    name VARCHAR(100) UNIQUE NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 4. Table user_groups
CREATE TABLE IF NOT EXISTS user_groups (
    user_id VARCHAR(36) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id VARCHAR(36) NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, group_id)
);

CREATE INDEX IF NOT EXISTS idx_user_groups_user ON user_groups(user_id);
CREATE INDEX IF NOT EXISTS idx_user_groups_group ON user_groups(group_id);

-- 5. Table database_connections (created before grants & migrations)
CREATE TABLE IF NOT EXISTS database_connections (
    id VARCHAR(36) PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    dbms_type VARCHAR(20) NOT NULL, -- 'postgres' or 'mysql'
    host VARCHAR(255) NOT NULL,
    port INT NOT NULL,
    db_name VARCHAR(100) NOT NULL,
    username VARCHAR(100) NOT NULL,
    password_encrypted TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 6. Table group_database_grants
CREATE TABLE IF NOT EXISTS group_database_grants (
    group_id VARCHAR(36) NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    database_id VARCHAR(36) NOT NULL REFERENCES database_connections(id) ON DELETE CASCADE,
    access_level VARCHAR(20) NOT NULL, -- 'VIEW_ONLY' or 'VIEW_EDIT'
    PRIMARY KEY (group_id, database_id)
);

CREATE INDEX IF NOT EXISTS idx_grants_group ON group_database_grants(group_id);
CREATE INDEX IF NOT EXISTS idx_grants_database ON group_database_grants(database_id);

-- 7. Table migrations
CREATE TABLE IF NOT EXISTS migrations (
    id VARCHAR(36) PRIMARY KEY,
    database_id VARCHAR(36) NOT NULL REFERENCES database_connections(id) ON DELETE CASCADE,
    version INT NOT NULL,
    sql_query TEXT NOT NULL,
    executed_by_user_id VARCHAR(36) REFERENCES users(id) ON DELETE SET NULL,
    executed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    status VARCHAR(20) NOT NULL, -- 'PENDING', 'SUCCESS', 'FAILED'
    error_message TEXT
);

CREATE INDEX IF NOT EXISTS idx_migrations_database_version ON migrations(database_id, version);
