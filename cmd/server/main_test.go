package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestFrontendRoutes(t *testing.T) {
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("SPA shell"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "app.js"), []byte("bundle"), 0600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerFrontend(mux, dist)
	for _, test := range []struct{ path, body string }{{"/login", "SPA shell"}, {"/admin/users", "SPA shell"}, {"/databases/db/history", "SPA shell"}, {"/jobs/job", "SPA shell"}, {"/app.js", "bundle"}} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", test.path, nil))
		if w.Code != 200 || w.Body.String() != test.body {
			t.Errorf("%s returned %d %q", test.path, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/missing", nil))
	if w.Code != 404 {
		t.Errorf("unknown API route returned %d", w.Code)
	}
}
