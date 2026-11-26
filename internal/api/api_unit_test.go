package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/admin"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/migration"
)

func TestConnectionJSONTagsAreSnakeCase(t *testing.T) {
	raw, err := json.Marshal(database.Connection{
		ID:       "conn-1",
		Name:     "target-postgres-1",
		DBMS:     "postgres",
		Host:     "127.0.0.1",
		Port:     5433,
		DBName:   "target_db",
		Username: "postgres",
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "name", "dbms", "host", "port", "db_name", "username"} {
		if _, ok := m[key]; !ok {
			t.Fatalf("missing expected snake_case key %q in Connection JSON: %s", key, string(raw))
		}
	}
}

func TestHandleDomainErrorStatusMapping(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{migration.ErrDefinitionNotFound, http.StatusNotFound},
		{migration.ErrNotGroupLeader, http.StatusForbidden},
		{migration.ErrImmutableDefinition, http.StatusConflict},
		{migration.ErrRollbackNotActiveTip, http.StatusConflict},
		{migration.ErrIrreversibleMigration, http.StatusConflict},
		{migration.ErrInvalidDefinition, http.StatusUnprocessableEntity},
		{migration.ErrPublisherNotConfigured, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		HandleDomainError(rec, tc.err)
		if rec.Code != tc.want {
			t.Errorf("HandleDomainError(%v) = %d, want %d", tc.err, rec.Code, tc.want)
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] == "" {
			t.Errorf("expected JSON error body for %v, got %s", tc.err, rec.Body.String())
		}
	}
}

func TestAPICSRFAndAdminMiddlewareReturnJSON(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
	})
	protected := auth.CSRFProtectionMiddleware(admin.RequireAdmin(next))

	// 1. POST /api/v1/admin/groups tanpa header CSRF mengembalikan JSON 403.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/groups", bytes.NewBufferString(`{"name":"team-a"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.ContextWithAuth(req.Context(),
		&auth.User{ID: "u1", Username: "admin", IsPlatformAdmin: true, IsActive: true},
		&auth.Session{ID: "s1", CSRFToken: "valid-csrf"},
	))
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("expected 403 JSON on missing CSRF, got %d (%s)", rec.Code, rec.Header().Get("Content-Type"))
	}

	// 2. Non-admin dengan X-CSRF-Token valid mengembalikan JSON 403.
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/admin/groups", bytes.NewBufferString(`{"name":"team-a"}`))
	req2.Header.Set("X-CSRF-Token", "valid-csrf")
	req2 = req2.WithContext(auth.ContextWithAuth(req2.Context(),
		&auth.User{ID: "u2", Username: "member", IsPlatformAdmin: false, IsActive: true},
		&auth.Session{ID: "s2", CSRFToken: "valid-csrf"},
	))
	rec2 := httptest.NewRecorder()
	protected.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin, got %d", rec2.Code)
	}

	// 3. Admin dengan X-CSRF-Token valid lolos.
	req3 := httptest.NewRequest(http.MethodPost, "/api/v1/admin/groups", bytes.NewBufferString(`{"name":"team-a"}`))
	req3.Header.Set("X-CSRF-Token", "valid-csrf")
	req3 = req3.WithContext(auth.ContextWithAuth(req3.Context(),
		&auth.User{ID: "u1", Username: "admin", IsPlatformAdmin: true, IsActive: true},
		&auth.Session{ID: "s1", CSRFToken: "valid-csrf"},
	))
	rec3 := httptest.NewRecorder()
	protected.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin with valid CSRF, got %d", rec3.Code)
	}
}

func TestSPAHandlerFallbackAndTraversalProtection(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>SPA-INDEX</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("console.log('ok')"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := NewSPAHandler(dir)

	// File statis disajikan langsung.
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if rec1.Code != http.StatusOK || !bytes.Contains(rec1.Body.Bytes(), []byte("console.log")) {
		t.Fatalf("expected static asset, got %d %s", rec1.Code, rec1.Body.String())
	}

	// Route SPA sisi klien diarahkan ke index.html.
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/databases/123/history", nil))
	if rec2.Code != http.StatusOK || !bytes.Contains(rec2.Body.Bytes(), []byte("SPA-INDEX")) {
		t.Fatalf("expected index.html fallback, got %d %s", rec2.Code, rec2.Body.String())
	}

	// Endpoint /api/* yang tidak dikenal mengembalikan JSON 404, bukan index.html.
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, httptest.NewRequest(http.MethodGet, "/api/v1/does-not-exist", nil))
	if rec3.Code != http.StatusNotFound || bytes.Contains(rec3.Body.Bytes(), []byte("SPA-INDEX")) {
		t.Fatalf("expected 404 JSON for /api/*, got %d %s", rec3.Code, rec3.Body.String())
	}

	// Percobaan path traversal aman kembali ke index.html tanpa membaca di luar distDir.
	rec4 := httptest.NewRecorder()
	h.ServeHTTP(rec4, httptest.NewRequest(http.MethodGet, "/../../go.mod", nil))
	if bytes.Contains(rec4.Body.Bytes(), []byte("module github.com")) {
		t.Fatal("path traversal leaked file outside SPA distDir!")
	}
}

func TestDecodeJSONValidationAndSizeLimit(t *testing.T) {
	var out map[string]string

	// Body kosong ditolak.
	r1 := httptest.NewRequest(http.MethodPost, "/api/v1/test", bytes.NewBufferString(""))
	if err := DecodeJSON(httptest.NewRecorder(), r1, 64, &out); err == nil {
		t.Fatal("expected error for empty JSON body")
	}
	// Body melebihi batas ukuran ditolak.
	r2 := httptest.NewRequest(http.MethodPost, "/api/v1/test", bytes.NewBufferString(`{"k":"`+string(bytes.Repeat([]byte("a"), 200))+`"}`))
	if err := DecodeJSON(httptest.NewRecorder(), r2, 64, &out); err == nil {
		t.Fatal("expected error for oversized JSON body")
	}
	// JSON valid diterima.
	r3 := httptest.NewRequest(http.MethodPost, "/api/v1/test", bytes.NewBufferString(`{"k":"ok"}`))
	if err := DecodeJSON(httptest.NewRecorder(), r3, 64, &out); err != nil || out["k"] != "ok" {
		t.Fatalf("expected valid decode, err=%v out=%#v", err, out)
	}
}

func TestRegisterRoutesHealthMetricsAndAuthGuard(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{})

	// GET /api/v1/health mengembalikan JSON 200.
	recHealth := httptest.NewRecorder()
	mux.ServeHTTP(recHealth, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if recHealth.Code != http.StatusOK || !bytes.Contains(recHealth.Body.Bytes(), []byte(`"ok"`)) {
		t.Fatalf("GET /api/v1/health = %d %s", recHealth.Code, recHealth.Body.String())
	}

	// GET /metrics pada router publik :8080 wajib terproteksi (401 tanpa sesi).
	recMetrics := httptest.NewRecorder()
	mux.ServeHTTP(recMetrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recMetrics.Code != http.StatusUnauthorized {
		t.Fatalf("GET /metrics without cookie = %d, want 401", recMetrics.Code)
	}

	// Endpoint API tanpa autentikasi mengembalikan JSON 401, bukan redirect HTML 303.
	for _, ep := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/auth/me"},
		{http.MethodGet, "/api/v1/databases"},
		{http.MethodGet, "/api/v1/requests"},
		{http.MethodPost, "/api/v1/requests/req-1/enqueue"},
		{http.MethodGet, "/api/v1/admin/groups"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(ep.method, ep.path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without cookie = %d, want 401", ep.method, ep.path, rec.Code)
		}
	}
}

func TestSeedSQLBcryptHashMatchesDevPass(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "seed_m2.sql"))
	if err != nil {
		t.Fatalf("read seed_m2.sql: %v", err)
	}
	const expectedHash = "$2a$10$U9HCGxTmtfFiVCpdeI6AhuEM2nMtwDjgFm6MmP3/5wcpl25SorpoO"
	if !bytes.Contains(raw, []byte(expectedHash)) {
		t.Fatalf("seed_m2.sql does not contain expected bcrypt hash")
	}
	if err := auth.CheckPassword(expectedHash, "DevPass!2026"); err != nil {
		t.Fatalf("seed_m2.sql hash does not match DevPass!2026: %v", err)
	}
}

func TestDecodeOptionalJSONAllowsEmptyRejectsMalformed(t *testing.T) {
	var body struct {
		Comment string `json:"comment"`
	}
	rEmpty := httptest.NewRequest(http.MethodPost, "/approve", bytes.NewReader(nil))
	if err := DecodeOptionalJSON(httptest.NewRecorder(), rEmpty, 1024, &body); err != nil {
		t.Fatalf("empty body unexpected err: %v", err)
	}
	rChunkedEOF := httptest.NewRequest(http.MethodPost, "/approve", bytes.NewBufferString(""))
	rChunkedEOF.ContentLength = -1
	if err := DecodeOptionalJSON(httptest.NewRecorder(), rChunkedEOF, 1024, &body); err != nil {
		t.Fatalf("empty chunked body unexpected err: %v", err)
	}
	rValid := httptest.NewRequest(http.MethodPost, "/approve", bytes.NewBufferString(`{"comment":"ok"}`))
	if err := DecodeOptionalJSON(httptest.NewRecorder(), rValid, 1024, &body); err != nil || body.Comment != "ok" {
		t.Fatalf("valid optional json err=%v body=%+v", err, body)
	}
	rBad := httptest.NewRequest(http.MethodPost, "/approve", bytes.NewBufferString(`{"comment":`))
	if err := DecodeOptionalJSON(httptest.NewRecorder(), rBad, 1024, &body); err == nil {
		t.Fatal("expected error for malformed optional json")
	}
}
