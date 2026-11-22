package api

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/telemetry"
)

// AuthAPIHandler menangani endpoint autentikasi JSON untuk SPA.
type AuthAPIHandler struct {
	DB           *sql.DB
	SecureCookie bool
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type authUserDTO struct {
	ID              string   `json:"id"`
	Username        string   `json:"username"`
	IsPlatformAdmin bool     `json:"is_platform_admin"`
	IsActive        bool     `json:"is_active"`
	LeaderGroupIDs  []string `json:"leader_group_ids"`
}

// Login menangani POST /api/v1/auth/login.
func (h *AuthAPIHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := DecodeJSON(w, r, 8*1024, &req); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	username := strings.TrimSpace(req.Username)
	password := req.Password
	if username == "" || password == "" {
		telemetry.LogAudit(r.Context(), "auth.login", "denied", slog.String("reason", "empty_credentials"))
		WriteError(w, http.StatusUnauthorized, "Username atau password salah")
		return
	}

	var (
		userID          string
		passwordHash    string
		isPlatformAdmin bool
		isActive        bool
	)
	err := h.DB.QueryRowContext(r.Context(), `
		SELECT id, password_hash, is_platform_admin, is_active
		FROM users
		WHERE username = $1
	`, username).Scan(&userID, &passwordHash, &isPlatformAdmin, &isActive)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			telemetry.LogAudit(r.Context(), "auth.login", "denied", slog.String("username", username))
			WriteError(w, http.StatusUnauthorized, "Username atau password salah")
			return
		}
		WriteError(w, http.StatusInternalServerError, "Gagal memverifikasi kredensial")
		return
	}

	if err := auth.CheckPassword(passwordHash, password); err != nil {
		telemetry.LogAudit(r.Context(), "auth.login", "denied", slog.String("username", username))
		WriteError(w, http.StatusUnauthorized, "Username atau password salah")
		return
	}
	if !isActive {
		telemetry.LogAudit(r.Context(), "auth.login", "denied", slog.String("username", username), slog.String("reason", "account_disabled"))
		WriteError(w, http.StatusForbidden, "Akun Anda telah dinonaktifkan oleh Administrator.")
		return
	}

	sess, err := auth.CreateSession(r.Context(), h.DB, userID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "Gagal membuat sesi pengguna")
		return
	}
	auth.SetSessionCookie(w, sess.ID, h.SecureCookie)

	leaderGroups, _ := h.leaderGroupIDs(r, userID)
	ctx := telemetry.WithActor(r.Context(), username)
	telemetry.LogAudit(ctx, "auth.login", "ok", slog.Bool("is_platform_admin", isPlatformAdmin))

	WriteJSON(w, http.StatusOK, map[string]any{
		"user": authUserDTO{
			ID:              userID,
			Username:        username,
			IsPlatformAdmin: isPlatformAdmin,
			IsActive:        isActive,
			LeaderGroupIDs:  leaderGroups,
		},
		"csrf_token": sess.CSRFToken,
	})
}

// Logout menangani POST /api/v1/auth/logout.
func (h *AuthAPIHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(auth.CookieSessionName); err == nil && cookie.Value != "" {
		if err := auth.RevokeSession(r.Context(), h.DB, cookie.Value); err != nil {
			WriteError(w, http.StatusServiceUnavailable, "Logout tidak dapat disimpan. Silakan coba kembali.")
			return
		}
	}
	auth.ClearSessionCookie(w, h.SecureCookie)
	telemetry.LogAudit(r.Context(), "auth.logout", "ok")
	WriteJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

// Me menangani GET /api/v1/auth/me.
func (h *AuthAPIHandler) Me(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUserFromContext(r.Context())
	sess := auth.GetSessionFromContext(r.Context())
	if user == nil || sess == nil {
		WriteError(w, http.StatusUnauthorized, "Authentication required")
		return
	}
	leaderGroups, _ := h.leaderGroupIDs(r, user.ID)
	WriteJSON(w, http.StatusOK, map[string]any{
		"user": authUserDTO{
			ID:              user.ID,
			Username:        user.Username,
			IsPlatformAdmin: user.IsPlatformAdmin,
			IsActive:        user.IsActive,
			LeaderGroupIDs:  leaderGroups,
		},
		"csrf_token": sess.CSRFToken,
	})
}

func (h *AuthAPIHandler) leaderGroupIDs(r *http.Request, userID string) ([]string, error) {
	rows, err := h.DB.QueryContext(r.Context(), `SELECT id FROM groups WHERE leader_user_id = $1 ORDER BY name ASC`, userID)
	if err != nil {
		return []string{}, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}
