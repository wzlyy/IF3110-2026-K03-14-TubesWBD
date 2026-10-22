package access

import (
	"database/sql"
	"net/http"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
)

// RequireDatabaseAccess enforces that the authenticated user possesses at least
// the required AccessLevel for the database specified in the request URL.
func RequireDatabaseAccess(db *sql.DB, requiredLevel AccessLevel) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := auth.GetUserFromContext(r.Context())
			if user == nil {
				http.Error(w, "Unauthorized: Authentication required", http.StatusUnauthorized)
				return
			}

			// Extract database ID from path value or query parameter
			dbID := r.PathValue("database_id")
			if dbID == "" {
				dbID = r.PathValue("id")
			}
			if dbID == "" {
				dbID = r.URL.Query().Get("database_id")
			}

			if dbID == "" {
				http.Error(w, "Bad Request: Missing database identifier", http.StatusBadRequest)
				return
			}

			effectiveLevel, err := GetEffectiveAccess(r.Context(), db, user.ID, dbID)
			if err != nil {
				http.Error(w, "Internal Server Error: Failed to evaluate permissions", http.StatusInternalServerError)
				return
			}

			if !IsHigherOrEqual(effectiveLevel, requiredLevel) {
				http.Error(w, "Forbidden: You do not have sufficient permissions for this database", http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
