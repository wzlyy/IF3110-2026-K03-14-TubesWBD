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
