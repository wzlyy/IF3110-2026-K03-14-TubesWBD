package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/telemetry"
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
// Untuk route /api/* kembalikan JSON 401/403 alih-alih redirect HTML.
// Menyisipkan User, Session, dan aktor telemetry ke dalam context request.
func RequireAuth(db *sql.DB, secureCookie bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			isAPI := strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/metrics"
			denyUnauth := func(msg string) {
				if isAPI {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
					return
				}
				http.Redirect(w, r, "/login", http.StatusSeeOther)
			}

			cookie, err := r.Cookie(CookieSessionName)
			if err != nil || cookie.Value == "" {
				denyUnauth("Authentication required")
				return
			}

			ctx := r.Context()
			sess, wasPrevious, err := GetSession(ctx, db, cookie.Value)
			if err != nil {
				ClearSessionCookie(w, secureCookie)
				denyUnauth("Session invalid or expired")
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
						if isAPI {
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(http.StatusServiceUnavailable)
							_, _ = w.Write([]byte(`{"error":"Session rotation unavailable. Please retry."}`))
							return
						}
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
					denyUnauth("Session user not found")
					return
				}
				if isAPI {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"error":"Internal Server Error"}`))
					return
				}
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}

			// Check if account has been disabled
			if !u.IsActive {
				_ = RevokeAllUserSessions(ctx, db, u.ID)
				ClearSessionCookie(w, secureCookie)
				if isAPI {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"error":"Account disabled by administrator"}`))
					return
				}
				http.Redirect(w, r, "/login?error=account_disabled", http.StatusSeeOther)
				return
			}

			// Sisipkan ke context termasuk aktor telemetry untuk log audit dan SQL
			ctx = context.WithValue(ctx, userCtxKey, &u)
			ctx = context.WithValue(ctx, sessionCtxKey, sess)
			ctx = telemetry.WithActor(ctx, u.Username)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ContextWithAuth menyisipkan User dan Session ke context (untuk pemanggilan handler langsung saat testing).
func ContextWithAuth(ctx context.Context, user *User, sess *Session) context.Context {
	if user != nil {
		ctx = context.WithValue(ctx, userCtxKey, user)
		ctx = telemetry.WithActor(ctx, user.Username)
	}
	if sess != nil {
		ctx = context.WithValue(ctx, sessionCtxKey, sess)
	}
	return ctx
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
