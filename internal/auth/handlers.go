package auth

import (
	"database/sql"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
)

// TemplateRenderer defines the interface for rendering templates.
type TemplateRenderer interface {
	ExecuteTemplate(w io.Writer, name string, data any) error
}

// AuthHandler handles authentication endpoints.
type AuthHandler struct {
	DB           *sql.DB
	Templates    TemplateRenderer
	SecureCookie bool
}

// ShowLogin renders the login page.
func (h *AuthHandler) ShowLogin(w http.ResponseWriter, r *http.Request) {
	// If already authenticated with a valid session, redirect to appropriate landing page
	if cookie, err := r.Cookie(CookieSessionName); err == nil && cookie.Value != "" {
		if sess, _, err := GetSession(r.Context(), h.DB, cookie.Value); err == nil && sess != nil {
			var isAdmin bool
			_ = h.DB.QueryRowContext(r.Context(), "SELECT is_platform_admin FROM users WHERE id = $1", sess.UserID).Scan(&isAdmin)
			if isAdmin {
				http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
			} else {
				http.Redirect(w, r, "/databases", http.StatusSeeOther)
			}
			return
		}
	}

	data := map[string]interface{}{
		"Error":   "",
		"Success": "",
	}

	if r.URL.Query().Get("error") == "account_disabled" {
		data["Error"] = "Akun Anda telah dinonaktifkan oleh Administrator."
	} else if r.URL.Query().Get("logged_out") == "true" {
		data["Success"] = "Anda telah berhasil logout."
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.Templates.ExecuteTemplate(w, "login.html", data); err != nil {
		log.Printf("Template render error: %v", err)
		http.Error(w, "Error rendering login page", http.StatusInternalServerError)
	}
}

// ProcessLogin processes the submitted login credentials.
func (h *AuthHandler) ProcessLogin(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.PostFormValue("username"))
	password := r.PostFormValue("password")

	// Sanitized failure response helper
	failLogin := func() {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_ = h.Templates.ExecuteTemplate(w, "login.html", map[string]interface{}{
			"Error":    "Username atau password salah",
			"Username": username,
		})
	}

	if username == "" || password == "" {
		failLogin()
		return
	}

	var (
		userID          string
		passwordHash    string
		isPlatformAdmin bool
		isActive        bool
	)

	query := `
		SELECT id, password_hash, is_platform_admin, is_active
		FROM users
		WHERE username = $1
	`
	err := h.DB.QueryRowContext(r.Context(), query, username).Scan(
		&userID,
		&passwordHash,
		&isPlatformAdmin,
		&isActive,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			failLogin()
			return
		}
		log.Printf("Database error during login query: %v", err)
		failLogin()
		return
	}

	// Verify password hash
	if err := CheckPassword(passwordHash, password); err != nil {
		failLogin()
		return
	}

	// Verify active status
	if !isActive {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_ = h.Templates.ExecuteTemplate(w, "login.html", map[string]interface{}{
			"Error": "Akun Anda telah dinonaktifkan oleh Administrator.",
		})
		return
	}

	// Create new session (opaque token)
	sess, err := CreateSession(r.Context(), h.DB, userID)
	if err != nil {
		log.Printf("Failed to create user session: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Set session cookie
	SetSessionCookie(w, sess.ID, h.SecureCookie)

	// Redirect based on role
	if isPlatformAdmin {
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	} else {
		http.Redirect(w, r, "/databases", http.StatusSeeOther)
	}
}

// ProcessLogout terminates the active session in DB and clears the cookie.
func (h *AuthHandler) ProcessLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(CookieSessionName); err == nil && cookie.Value != "" {
		_ = RevokeSession(r.Context(), h.DB, cookie.Value)
	}

	ClearSessionCookie(w, h.SecureCookie)
	http.Redirect(w, r, "/login?logged_out=true", http.StatusSeeOther)
}
