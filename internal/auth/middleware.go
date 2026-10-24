package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"
)

type contextKey string

const (
	userCtxKey    contextKey = "auth_user"
	sessionCtxKey contextKey = "auth_session"
)

// User represents an authenticated platform user.
type User struct {
	ID              string
	Username        string
	IsPlatformAdmin bool
	IsActive        bool
	CreatedAt       time.Time
}

// RequireAuth returns a middleware requiring a valid session.
// Injects the User and Session into the request context.
func RequireAuth(db *sql.DB, secureCookie bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(CookieSessionName)
			if err != nil || cookie.Value == "" {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}

			ctx := r.Context()
			sess, wasPrevious, err := GetSession(ctx, db, cookie.Value)
			if err != nil {
				ClearSessionCookie(w, secureCookie)
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}

			// If client presented the rotated previous token in grace period, refresh cookie to new token
			if wasPrevious {
				SetSessionCookie(w, sess.ID, secureCookie)
			} else {
				// Check periodic rotation: every 15 minutes of activity
				if time.Now().UTC().Sub(sess.LastRotatedAt) >= RotationInterval {
					rotated, err := RotateSession(ctx, db, sess.ID)
					if err != nil && errors.Is(err, sql.ErrNoRows) {
						// Another request may have rotated this token first.
						rotated, _, err = GetSession(ctx, db, sess.ID)
					}
					if err != nil {
						http.Error(w, "Session rotation unavailable. Please retry.", http.StatusServiceUnavailable)
						return
					}
					sess = rotated
					SetSessionCookie(w, sess.ID, secureCookie)
				}
			}

			// Fetch user details from database
			var u User
			query := `
				SELECT id, username, is_platform_admin, is_active, created_at
				FROM users
				WHERE id = $1
			`
			err = db.QueryRowContext(ctx, query, sess.UserID).Scan(
				&u.ID,
				&u.Username,
				&u.IsPlatformAdmin,
				&u.IsActive,
				&u.CreatedAt,
			)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					_ = RevokeSession(ctx, db, sess.ID)
					ClearSessionCookie(w, secureCookie)
					http.Redirect(w, r, "/login", http.StatusSeeOther)
					return
				}
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}

			// Check if account has been disabled
			if !u.IsActive {
				_ = RevokeAllUserSessions(ctx, db, u.ID)
				ClearSessionCookie(w, secureCookie)
				http.Redirect(w, r, "/login?error=account_disabled", http.StatusSeeOther)
				return
			}

			// Attach to context
			ctx = context.WithValue(ctx, userCtxKey, &u)
			ctx = context.WithValue(ctx, sessionCtxKey, sess)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetUserFromContext retrieves the authenticated User from context.
func GetUserFromContext(ctx context.Context) *User {
	if val, ok := ctx.Value(userCtxKey).(*User); ok {
		return val
	}
	return nil
}

// GetSessionFromContext retrieves the authenticated Session from context.
func GetSessionFromContext(ctx context.Context) *Session {
	if val, ok := ctx.Value(sessionCtxKey).(*Session); ok {
		return val
	}
	return nil
}
