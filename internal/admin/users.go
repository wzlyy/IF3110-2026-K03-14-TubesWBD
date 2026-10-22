package admin

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
)

// UserViewModel represents a user record formatted for display in the admin panel.
type UserViewModel struct {
	ID              string
	Username        string
	IsPlatformAdmin bool
	IsActive        bool
	CreatedAt       time.Time
	GroupNames      []string
}

// UserAdminHandler handles administration operations on user accounts.
type UserAdminHandler struct {
	DB        *sql.DB
	Templates auth.TemplateRenderer
}

// ListUsers renders the user management dashboard.
func (h *UserAdminHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	currentUser := auth.GetUserFromContext(ctx)
	currentSession := auth.GetSessionFromContext(ctx)

	rows, err := h.DB.QueryContext(ctx, `
		SELECT u.id, u.username, u.is_platform_admin, u.is_active, u.created_at,
		       COALESCE(string_agg(g.name, ', '), '') AS groups
		FROM users u
		LEFT JOIN user_groups ug ON u.id = ug.user_id
		LEFT JOIN groups g ON ug.group_id = g.id
		GROUP BY u.id
		ORDER BY u.created_at ASC
	`)
	if err != nil {
		log.Printf("Failed to query users: %v", err)
		http.Error(w, "Failed to load users", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var users []UserViewModel
	for rows.Next() {
		var u UserViewModel
		var rawGroups string
		if err := rows.Scan(&u.ID, &u.Username, &u.IsPlatformAdmin, &u.IsActive, &u.CreatedAt, &rawGroups); err != nil {
			log.Printf("Scan user error: %v", err)
			continue
		}
		if rawGroups != "" {
			u.GroupNames = strings.Split(rawGroups, ", ")
		}
		users = append(users, u)
	}

	data := map[string]interface{}{
		"CurrentUser":    currentUser,
		"CSRFToken":      currentSession.CSRFToken,
		"Users":          users,
		"ActiveTab":      "users",
		"SuccessMessage": r.URL.Query().Get("success"),
		"ErrorMessage":   r.URL.Query().Get("error"),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.Templates.ExecuteTemplate(w, "users.html", data); err != nil {
		log.Printf("Render error: %v", err)
		http.Error(w, "Render error", http.StatusInternalServerError)
	}
}

// CreateUser creates a new platform user.
func (h *UserAdminHandler) CreateUser(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.PostFormValue("username"))
	password := r.PostFormValue("password")
	isAdmin := r.PostFormValue("is_platform_admin") == "true" || r.PostFormValue("is_platform_admin") == "1"

	if username == "" || password == "" {
		http.Redirect(w, r, "/admin/users?error=Username+dan+password+wajib+diisi", http.StatusSeeOther)
		return
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		http.Redirect(w, r, "/admin/users?error=Password+minimal+6+karakter", http.StatusSeeOther)
		return
	}

	userID := uuid.New().String()
	query := `
		INSERT INTO users (id, username, password_hash, is_platform_admin, is_active, created_at)
		VALUES ($1, $2, $3, $4, TRUE, CURRENT_TIMESTAMP)
	`
	_, err = h.DB.ExecContext(r.Context(), query, userID, username, hash, isAdmin)
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			http.Redirect(w, r, "/admin/users?error=Username+sudah+terdaftar", http.StatusSeeOther)
			return
		}
		log.Printf("Failed to create user: %v", err)
		http.Redirect(w, r, "/admin/users?error=Gagal+membuat+pengguna", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/users?success=Pengguna+berhasil+dibuat", http.StatusSeeOther)
}

// ToggleActive toggles a user's active status.
// Deactivating a user immediately revokes all their backend sessions.
func (h *UserAdminHandler) ToggleActive(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	if userID == "" {
		http.Redirect(w, r, "/admin/users?error=ID+pengguna+tidak+valid", http.StatusSeeOther)
		return
	}

	ctx := r.Context()
	var currentActive bool
	err := h.DB.QueryRowContext(ctx, "SELECT is_active FROM users WHERE id = $1", userID).Scan(&currentActive)
	if err != nil {
		http.Redirect(w, r, "/admin/users?error=Pengguna+tidak+ditemukan", http.StatusSeeOther)
		return
	}

	newActive := !currentActive
	_, err = h.DB.ExecContext(ctx, "UPDATE users SET is_active = $1 WHERE id = $2", newActive, userID)
	if err != nil {
		http.Redirect(w, r, "/admin/users?error=Gagal+mengubah+status+pengguna", http.StatusSeeOther)
		return
	}

	// If deactivated, revoke all active sessions to force re-login
	if !newActive {
		_ = auth.RevokeAllUserSessions(ctx, h.DB, userID)
	}

	http.Redirect(w, r, "/admin/users?success=Status+pengguna+berhasil+diperbarui", http.StatusSeeOther)
}

// ToggleAdmin promotes or demotes a user from Platform Admin.
// Promotion triggers session rotation (without logging out).
// Demotion revokes all sessions (forcing re-login).
func (h *UserAdminHandler) ToggleAdmin(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	if userID == "" {
		http.Redirect(w, r, "/admin/users?error=ID+pengguna+tidak+valid", http.StatusSeeOther)
		return
	}

	ctx := r.Context()
	var currentAdmin bool
	err := h.DB.QueryRowContext(ctx, "SELECT is_platform_admin FROM users WHERE id = $1", userID).Scan(&currentAdmin)
	if err != nil {
		http.Redirect(w, r, "/admin/users?error=Pengguna+tidak+ditemukan", http.StatusSeeOther)
		return
	}

	newAdmin := !currentAdmin
	_, err = h.DB.ExecContext(ctx, "UPDATE users SET is_platform_admin = $1 WHERE id = $2", newAdmin, userID)
	if err != nil {
		http.Redirect(w, r, "/admin/users?error=Gagal+mengubah+peran+admin", http.StatusSeeOther)
		return
	}

	if newAdmin {
		// Privilege escalation: rotate session without forcing logout
		_ = auth.RotateUserSessions(ctx, h.DB, userID)
	} else {
		// Privilege revocation: revoke all sessions
		_ = auth.RevokeAllUserSessions(ctx, h.DB, userID)
	}

	http.Redirect(w, r, "/admin/users?success=Peran+admin+berhasil+diperbarui", http.StatusSeeOther)
}

// ResetPassword resets a user's password and revokes their existing sessions.
func (h *UserAdminHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	newPassword := r.PostFormValue("new_password")

	if userID == "" || len(newPassword) < 6 {
		http.Redirect(w, r, "/admin/users?error=Password+baru+minimal+6+karakter", http.StatusSeeOther)
		return
	}

	ctx := r.Context()
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		http.Redirect(w, r, "/admin/users?error=Gagal+memproses+password", http.StatusSeeOther)
		return
	}

	_, err = h.DB.ExecContext(ctx, "UPDATE users SET password_hash = $1 WHERE id = $2", hash, userID)
	if err != nil {
		http.Redirect(w, r, "/admin/users?error=Gagal+mengubah+password", http.StatusSeeOther)
		return
	}

	// Revoke sessions upon password change for security
	_ = auth.RevokeAllUserSessions(ctx, h.DB, userID)

	http.Redirect(w, r, fmt.Sprintf("/admin/users?success=Password+berhasil+diperbarui"), http.StatusSeeOther)
}
