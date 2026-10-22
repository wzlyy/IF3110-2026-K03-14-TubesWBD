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
	ID           string
	UserID       string
	CreatedAt    time.Time
	LastActiveAt time.Time
	ExpiresAt    time.Time
	PreviousID   sql.NullString
	GraceUntil   sql.NullTime
	CSRFToken    string
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
		INSERT INTO sessions (id, user_id, created_at, last_active_at, expires_at, csrf_token)
		VALUES ($1, $2, $3, $4, $5, $6)
	`

	_, err = db.ExecContext(ctx, query, token, userID, now, now, expiresAt, csrfToken)
	if err != nil {
		return nil, fmt.Errorf("failed to insert session: %w", err)
	}

	return &Session{
		ID:           token,
		UserID:       userID,
		CreatedAt:    now,
		LastActiveAt: now,
		ExpiresAt:    expiresAt,
		CSRFToken:    csrfToken,
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

	// Query by either current ID or previous ID
	query := `
		SELECT id, user_id, created_at, last_active_at, expires_at, previous_id, grace_until, csrf_token
		FROM sessions
		WHERE id = $1 OR previous_id = $1
	`

	var s Session
	err := db.QueryRowContext(ctx, query, token).Scan(
		&s.ID,
		&s.UserID,
		&s.CreatedAt,
		&s.LastActiveAt,
		&s.ExpiresAt,
		&s.PreviousID,
		&s.GraceUntil,
		&s.CSRFToken,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, ErrSessionNotFound
		}
		return nil, false, fmt.Errorf("session query error: %w", err)
	}

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
	wasPrevious := false
	if s.PreviousID.Valid && s.PreviousID.String == token {
		wasPrevious = true
		// Verify if grace period is still valid
		if !s.GraceUntil.Valid || now.After(s.GraceUntil.Time) {
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
// The current token becomes previous_id with a 30-second grace period.
func RotateSession(ctx context.Context, db *sql.DB, currentToken string) (*Session, error) {
	newToken, err := GenerateOpaqueToken()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	graceUntil := now.Add(GracePeriod)

	query := `
		UPDATE sessions
		SET previous_id = id,
		    id = $1,
		    grace_until = $2,
		    last_active_at = $3
		WHERE id = $4
		RETURNING id, user_id, created_at, last_active_at, expires_at, previous_id, grace_until, csrf_token
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
	)
	if err != nil {
		return nil, fmt.Errorf("failed to rotate session: %w", err)
	}

	return &s, nil
}

// RotateUserSessions rotates all active sessions for a user (used upon privilege escalation).
func RotateUserSessions(ctx context.Context, db *sql.DB, userID string) error {
	rows, err := db.QueryContext(ctx, "SELECT id FROM sessions WHERE user_id = $1", userID)
	if err != nil {
		return err
	}
	defer rows.Close()

	var tokens []string
	for rows.Next() {
		var tok string
		if err := rows.Scan(&tok); err == nil {
			tokens = append(tokens, tok)
		}
	}

	for _, tok := range tokens {
		_, _ = RotateSession(ctx, db, tok)
	}
	return nil
}

// RevokeSession deletes a single session by its current or previous identifier.
func RevokeSession(ctx context.Context, db *sql.DB, token string) error {
	query := `DELETE FROM sessions WHERE id = $1 OR previous_id = $1`
	_, err := db.ExecContext(ctx, query, token)
	return err
}

// RevokeAllUserSessions revokes all sessions for a specific user (used upon privilege revocation).
func RevokeAllUserSessions(ctx context.Context, db *sql.DB, userID string) error {
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
