package admin

import (
	"net/http"
	"strings"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
)

// RequireAdmin ensures that the authenticated user has Platform Admin privileges.
// Non-admin users are strictly denied with HTTP 403 Forbidden.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isAPI := strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/metrics"
		user := auth.GetUserFromContext(r.Context())
		if user == nil {
			if isAPI {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"Authentication required"}`))
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		if !user.IsPlatformAdmin {
			if isAPI {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":"Forbidden: Platform Administrator privileges required"}`))
				return
			}
			http.Error(w, "Forbidden: Platform Administrator privileges required", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}
