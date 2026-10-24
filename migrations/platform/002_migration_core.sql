-- Additive and idempotent: preserve every existing attempt.
ALTER TABLE migrations ADD COLUMN IF NOT EXISTS started_at TIMESTAMPTZ;
ALTER TABLE migrations ADD COLUMN IF NOT EXISTS finished_at TIMESTAMPTZ;
ALTER TABLE migrations ADD COLUMN IF NOT EXISTS partial_changes BOOLEAN NOT NULL DEFAULT FALSE;
UPDATE migrations SET started_at=executed_at WHERE started_at IS NULL;
ALTER TABLE migrations ALTER COLUMN started_at SET DEFAULT CURRENT_TIMESTAMP;
ALTER TABLE migrations ALTER COLUMN started_at SET NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_migrations_success_version
    ON migrations(database_id, version) WHERE status='SUCCESS';
