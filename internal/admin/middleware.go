package admin

import (
	"net/http"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
)

// RequireAdmin ensures that the authenticated user has Platform Admin privileges.
// Non-admin users are strictly denied with HTTP 403 Forbidden.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := auth.GetUserFromContext(r.Context())
		if user == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		if !user.IsPlatformAdmin {
			http.Error(w, "Forbidden: Platform Administrator privileges required", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}
