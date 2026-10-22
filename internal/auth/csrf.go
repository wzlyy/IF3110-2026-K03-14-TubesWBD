package auth

import (
	"crypto/subtle"
	"net/http"
)

// ValidateCSRF checks if the request contains a CSRF token matching the session's token.
// Checks both the 'csrf_token' form field and the 'X-CSRF-Token' request header.
func ValidateCSRF(session *Session, r *http.Request) bool {
	if session == nil || session.CSRFToken == "" {
		return false
	}

	providedToken := r.PostFormValue("csrf_token")
	if providedToken == "" {
		providedToken = r.Header.Get("X-CSRF-Token")
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

			// Skip CSRF check for login itself if session is not yet established
			if r.URL.Path == "/login" {
				next.ServeHTTP(w, r)
				return
			}

			session := GetSessionFromContext(r.Context())
			if session == nil || !ValidateCSRF(session, r) {
				http.Error(w, "Forbidden: Invalid or missing CSRF token", http.StatusForbidden)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}
