-- 1. Pemimpin grup (wajib M2: setiap grup memiliki leader untuk persetujuan)
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS leader_user_id VARCHAR(36) REFERENCES users(id) ON DELETE RESTRICT;

CREATE INDEX IF NOT EXISTS idx_groups_leader ON groups(leader_user_id);

-- 2. Definisi migrasi (immutable setelah percobaan eksekusi pertama dimulai)
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

-- 3. Permintaan perubahan dan alur persetujuan leader multi-grup
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

-- 4. Riwayat migrasi (append-only, mencatat semua percobaan eksekusi)
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

-- 5. Lock terdistribusi (lease, heartbeat, dan penahanan status partial/uncertain)
-- Catatan: execution_id disimpan sebagai UUID logis tanpa FK ke migration_executions
-- agar worker bisa mengunci dan memvalidasi ulang sebelum membuat baris riwayat.
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

-- 6. Salinan progres batch (disinkronkan dari _platform_meta.batch_checkpoints pada DB target)
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
