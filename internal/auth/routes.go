package auth

import "net/http"

// RegisterRoutes is shared by the server and HTTP tests, so logout cannot bypass
// the state-changing request policy while its form still contains a CSRF token.
func (h *AuthHandler) RegisterRoutes(mux *http.ServeMux, requireAuth func(http.Handler) http.Handler) {
	mux.HandleFunc("GET /login", h.ShowLogin)
	mux.HandleFunc("POST /login", h.ProcessLogin)
	mux.Handle("POST /logout", requireAuth(CSRFProtectionMiddleware(http.HandlerFunc(h.ProcessLogout))))
}
