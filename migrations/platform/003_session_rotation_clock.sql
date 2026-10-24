ALTER TABLE sessions ADD COLUMN IF NOT EXISTS last_rotated_at TIMESTAMPTZ;
UPDATE sessions SET last_rotated_at=created_at WHERE last_rotated_at IS NULL;
ALTER TABLE sessions ALTER COLUMN last_rotated_at SET DEFAULT CURRENT_TIMESTAMP;
ALTER TABLE sessions ALTER COLUMN last_rotated_at SET NOT NULL;
