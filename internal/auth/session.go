package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const (
	CookieSessionName = "session_id"
	IdleTimeout       = 30 * time.Minute
	AbsoluteTimeout   = 8 * time.Hour
	RotationInterval  = 15 * time.Minute
	GracePeriod       = 30 * time.Second
)

var (
	ErrSessionNotFound = errors.New("session not found")
	ErrSessionExpired  = errors.New("session expired")
)

// Session represents an authenticated user's session in the backend.
type Session struct {
	ID            string
	UserID        string
	CreatedAt     time.Time
	LastActiveAt  time.Time
	LastRotatedAt time.Time
	ExpiresAt     time.Time
	PreviousID    sql.NullString
	GraceUntil    sql.NullTime
	CSRFToken     string
}

// GenerateOpaqueToken generates a cryptographically secure random opaque token (32 bytes = 64 hex characters).
func GenerateOpaqueToken() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	if err != nil {
		return "", fmt.Errorf("failed to generate random token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// CreateSession generates a new session for the given userID and stores it in the database.
func CreateSession(ctx context.Context, db *sql.DB, userID string) (*Session, error) {
	token, err := GenerateOpaqueToken()
	if err != nil {
		return nil, err
	}

	csrfToken, err := GenerateOpaqueToken()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	expiresAt := now.Add(AbsoluteTimeout)

	query := `
		INSERT INTO sessions (id, user_id, created_at, last_active_at, expires_at, csrf_token,last_rotated_at)
		VALUES ($1, $2, $3, $4, $5, $6,$3)
	`

	_, err = db.ExecContext(ctx, query, token, userID, now, now, expiresAt, csrfToken)
	if err != nil {
		return nil, fmt.Errorf("failed to insert session: %w", err)
	}

	return &Session{
		ID:            token,
		UserID:        userID,
		CreatedAt:     now,
		LastActiveAt:  now,
		LastRotatedAt: now,
		ExpiresAt:     expiresAt,
		CSRFToken:     csrfToken,
	}, nil
}

// GetSession validates a session token against the database, enforcing idle and absolute timeouts,
// and honoring the grace period for rotated tokens.
// Returns (session, wasRotatedPrevious, error).
func GetSession(ctx context.Context, db *sql.DB, token string) (*Session, bool, error) {
	if token == "" {
		return nil, false, ErrSessionNotFound
	}

	now := time.Now().UTC()

	// Resolve every grace token directly to the canonical session. The immediate
	// previous token still uses sessions.grace_until for legacy compatibility.
	query := `
		SELECT s.id, s.user_id, s.created_at, s.last_active_at, s.expires_at, s.previous_id, s.grace_until, s.csrf_token,s.last_rotated_at,g.expires_at
		FROM sessions s
		LEFT JOIN session_grace_tokens g ON g.session_id=s.id AND g.token=$1
		WHERE s.id = $1 OR s.previous_id = $1 OR g.token = $1
	`

	var s Session
	var aliasExpiresAt sql.NullTime
	err := db.QueryRowContext(ctx, query, token).Scan(
		&s.ID,
		&s.UserID,
		&s.CreatedAt,
		&s.LastActiveAt,
		&s.ExpiresAt,
		&s.PreviousID,
		&s.GraceUntil,
		&s.CSRFToken,
		&s.LastRotatedAt,
		&aliasExpiresAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, ErrSessionNotFound
		}
		return nil, false, fmt.Errorf("session query error: %w", err)
	}
	_, _ = db.ExecContext(ctx, "DELETE FROM session_grace_tokens WHERE session_id=$1 AND expires_at<=$2", s.ID, now)

	// 1. Check Absolute Expiration (8 hours from creation)
	if now.After(s.ExpiresAt) {
		_ = RevokeSession(ctx, db, s.ID)
		return nil, false, ErrSessionExpired
	}

	// 2. Check Idle Timeout (30 minutes of inactivity)
	if now.Sub(s.LastActiveAt) > IdleTimeout {
		_ = RevokeSession(ctx, db, s.ID)
		return nil, false, ErrSessionExpired
	}

	// 3. Check if the token presented was the previous (grace period) token
	wasPrevious := token != s.ID
	if wasPrevious {
		deadline := aliasExpiresAt
		if s.PreviousID.Valid && s.PreviousID.String == token {
			deadline = s.GraceUntil
		}
		if !deadline.Valid || !now.Before(deadline.Time) {
			// Grace period expired for old token
			return nil, false, ErrSessionExpired
		}
		// In grace period: valid, but do not touch last_active_at since client should use new token
		return &s, true, nil
	}

	// 4. Update last_active_at for active session
	updateQuery := `UPDATE sessions SET last_active_at = $1 WHERE id = $2`
	_, _ = db.ExecContext(ctx, updateQuery, now, s.ID)
	s.LastActiveAt = now

	return &s, wasPrevious, nil
}

// RotateSession rotates the session identifier while keeping created_at and expires_at unchanged.
// Each rotated token retains its own 30-second grace period, including when
// another rotation happens before that grace period ends.
func RotateSession(ctx context.Context, db SessionStore, currentToken string) (*Session, error) {
	newToken, err := GenerateOpaqueToken()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	graceUntil := now.Add(GracePeriod)

	// Permission changes already supply their transaction. Standalone rotations
	// must also commit the canonical identifier and grace tokens together.
	var tx *sql.Tx
	if pool, ok := db.(*sql.DB); ok {
		tx, err = pool.BeginTx(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to begin session rotation: %w", err)
		}
		defer tx.Rollback()
		db = tx
	}
	var previousID sql.NullString
	var previousDeadline sql.NullTime
	if err := db.QueryRowContext(ctx, "SELECT previous_id,grace_until FROM sessions WHERE id=$1 FOR UPDATE", currentToken).Scan(&previousID, &previousDeadline); err != nil {
		return nil, fmt.Errorf("failed to lock session for rotation: %w", err)
	}
	// Keep a pre-upgrade previous token, and honor a shortened legacy deadline
	// before it becomes an older alias on this rotation.
	if previousID.Valid {
		if previousDeadline.Valid {
			if _, err := db.ExecContext(ctx, `INSERT INTO session_grace_tokens(token,session_id,expires_at) VALUES($1,$2,$3)
				ON CONFLICT(token) DO UPDATE SET expires_at=LEAST(session_grace_tokens.expires_at,EXCLUDED.expires_at)`, previousID.String, currentToken, previousDeadline.Time); err != nil {
				return nil, fmt.Errorf("failed to preserve previous session grace: %w", err)
			}
		} else if _, err := db.ExecContext(ctx, "DELETE FROM session_grace_tokens WHERE token=$1 AND session_id=$2", previousID.String, currentToken); err != nil {
			return nil, fmt.Errorf("failed to discard invalid previous session grace: %w", err)
		}
	}

	query := `
		UPDATE sessions
		SET previous_id = id,
		    id = $1,
		    grace_until = $2,
		    last_active_at = $3,
		    last_rotated_at = $3
		WHERE id = $4
		RETURNING id, user_id, created_at, last_active_at, expires_at, previous_id, grace_until, csrf_token,last_rotated_at
	`

	var s Session
	err = db.QueryRowContext(ctx, query, newToken, graceUntil, now, currentToken).Scan(
		&s.ID,
		&s.UserID,
		&s.CreatedAt,
		&s.LastActiveAt,
		&s.ExpiresAt,
		&s.PreviousID,
		&s.GraceUntil,
		&s.CSRFToken,
		&s.LastRotatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to rotate session: %w", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO session_grace_tokens(token,session_id,expires_at) VALUES($1,$2,$3)", currentToken, s.ID, graceUntil); err != nil {
		return nil, fmt.Errorf("failed to retain rotated session grace: %w", err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM session_grace_tokens WHERE session_id=$1 AND expires_at<=$2", s.ID, now); err != nil {
		return nil, fmt.Errorf("failed to clean expired session grace: %w", err)
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("failed to commit session rotation: %w", err)
		}
	}

	return &s, nil
}

// RotateUserSessions rotates all active sessions for a user (used upon privilege escalation).
func RotateUserSessions(ctx context.Context, db SessionStore, userID string) error {
	rows, err := db.QueryContext(ctx, "SELECT id FROM sessions WHERE user_id = $1", userID)
	if err != nil {
		return err
	}
	defer rows.Close()

	var tokens []string
	for rows.Next() {
		var tok string
		if err := rows.Scan(&tok); err != nil {
			return err
		}
		tokens = append(tokens, tok)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, tok := range tokens {
		if _, err := RotateSession(ctx, db, tok); err != nil {
			return err
		}
	}
	return nil
}

// RevokeSession deletes a canonical session and all grace tokens through any identifier.
func RevokeSession(ctx context.Context, db *sql.DB, token string) error {
	query := `DELETE FROM sessions WHERE id = $1 OR previous_id = $1
		OR id IN (SELECT session_id FROM session_grace_tokens WHERE token=$1)`
	_, err := db.ExecContext(ctx, query, token)
	return err
}

// RevokeAllUserSessions revokes all sessions for a specific user (used upon privilege revocation).
func RevokeAllUserSessions(ctx context.Context, db SessionStore, userID string) error {
	query := `DELETE FROM sessions WHERE user_id = $1`
	_, err := db.ExecContext(ctx, query, userID)
	return err
}

// SetSessionCookie writes the session cookie with secure flags.
func SetSessionCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieSessionName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(AbsoluteTimeout.Seconds()),
	})
}

// ClearSessionCookie clears the session cookie from the client browser.
func ClearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieSessionName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
}
