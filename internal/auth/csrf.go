package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// ValidateCSRF checks if the request contains a CSRF token matching the session's token.
// Memeriksa header 'X-CSRF-Token' terlebih dahulu (aman untuk SPA JSON), lalu fallback ke field form 'csrf_token'.
func ValidateCSRF(session *Session, r *http.Request) bool {
	if session == nil || session.CSRFToken == "" {
		return false
	}

	providedToken := r.Header.Get("X-CSRF-Token")
	if providedToken == "" {
		providedToken = r.PostFormValue("csrf_token")
	}

	if providedToken == "" {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(providedToken), []byte(session.CSRFToken)) == 1
}

// CSRFProtectionMiddleware ensures state-changing requests (POST, PUT, DELETE, PATCH)
// include a valid CSRF token matching the authenticated session.
func CSRFProtectionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only check state-changing methods
		if r.Method == http.MethodPost || r.Method == http.MethodPut ||
			r.Method == http.MethodDelete || r.Method == http.MethodPatch {

			// Lewati cek CSRF untuk endpoint login karena sesi belum terbentuk
			if r.URL.Path == "/login" || r.URL.Path == "/api/v1/auth/login" {
				next.ServeHTTP(w, r)
				return
			}

			session := GetSessionFromContext(r.Context())
			if session == nil || !ValidateCSRF(session, r) {
				if strings.HasPrefix(r.URL.Path, "/api/") {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"error":"Forbidden: Invalid or missing CSRF token"}`))
					return
				}
				http.Error(w, "Forbidden: Invalid or missing CSRF token", http.StatusForbidden)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}
