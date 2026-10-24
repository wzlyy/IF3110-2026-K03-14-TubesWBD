//go:build integration

package migration

import (
	"context"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
)

type pageRenderer map[string]*template.Template

func (p pageRenderer) ExecuteTemplate(w io.Writer, name string, data any) error {
	return p[name].ExecuteTemplate(w, "layout.html", data)
}

type httpFixture struct {
	*fixture
	mux     *http.ServeMux
	session *auth.Session
	base    string
}

func migrationHTTPFixture(t *testing.T, dbms, level string) *httpFixture {
	t.Helper()
	f := migrationFixture(t, dbms)
	pages := pageRenderer{}
	for _, page := range []string{"editor", "history", "detail"} {
		parsed, err := template.ParseFiles("../../web/templates/layout.html", "../../web/templates/migration/"+page+".html")
		if err != nil {
			t.Fatal(err)
		}
		pages["migration-"+page+".html"] = parsed
	}
	mux := http.NewServeMux()
	h := &Handler{DB: f.db, Store: f.store, Runner: f.runner, Templates: pages}
	h.RegisterRoutes(mux, auth.RequireAuth(f.db, false))
	group := uuid.NewString()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{"INSERT INTO groups(id,name) VALUES($1,$1)", []any{group}},
		{"INSERT INTO user_groups(user_id,group_id) VALUES($1,$2)", []any{f.user, group}},
		{"INSERT INTO group_database_grants(group_id,database_id,access_level) VALUES($1,$2,$3)", []any{group, f.connection.ID, level}},
	} {
		if _, err := f.db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	session, err := auth.CreateSession(context.Background(), f.db, f.user)
	if err != nil {
		t.Fatal(err)
	}
	return &httpFixture{f, mux, session, "/databases/" + f.connection.ID + "/migration"}
}

func (f *httpFixture) request(method, path string, form url.Values, csrf bool) *httptest.ResponseRecorder {
	if form == nil {
		form = url.Values{}
	}
	if csrf {
		form.Set("csrf_token", f.session.CSRFToken)
	}
	r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: auth.CookieSessionName, Value: f.session.ID})
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

func TestViewOnlyMigrationDenied(t *testing.T) {
	f := migrationHTTPFixture(t, "postgres", "VIEW_ONLY")
	for _, action := range []string{"", "/preview", "/execute", "/missing/retry", "/missing/reconcile"} {
		method := "POST"
		if action == "" {
			method = "GET"
		}
		w := f.request(method, f.base+action, url.Values{"sql_query": {"SELECT 1"}}, true)
		if w.Code != 403 {
			t.Errorf("%s %s: %d", method, action, w.Code)
		}
	}
	// Platform admins have no implicit target grant.
	if _, err := f.db.Exec("UPDATE users SET is_platform_admin=true WHERE id=$1", f.user); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("DELETE FROM group_database_grants WHERE database_id=$1", f.connection.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{f.base, f.base + "/history", f.base + "/missing"} {
		if w := f.request("GET", path, nil, false); w.Code != 403 {
			t.Errorf("admin without grant %s: %d", path, w.Code)
		}
	}
}

func TestViewOnlyCanViewHistory(t *testing.T) {
	f := migrationHTTPFixture(t, "postgres", "VIEW_ONLY")
	w := f.request("GET", f.base+"/history", nil, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Belum ada riwayat") {
		t.Fatalf("empty history: %d %s", w.Code, w.Body.String())
	}
	a := f.execute(t, "SELECT '<script>alert(1)</script>'")
	for _, path := range []string{f.base + "/history", f.base + "/" + a.ID} {
		w = f.request("GET", path, nil, false)
		body := w.Body.String()
		if w.Code != 200 || strings.Contains(body, "<script>alert(1)</script>") || !strings.Contains(body, "&lt;script&gt;") || strings.Contains(body, "Rekonsiliasi</button>") || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("read SSR: %d %s", w.Code, body)
		}
	}
	if w := f.request("GET", "/databases/unknown/migration/"+a.ID, nil, false); w.Code != 403 {
		t.Fatalf("cross-database: %d", w.Code)
	}
}

func TestMigrationEditorPreviewExecuteHistory(t *testing.T) {
	for _, dbms := range []string{"postgres", "mysql"} {
		t.Run(dbms, func(t *testing.T) {
			f := migrationHTTPFixture(t, dbms, "VIEW_EDIT")
			w := f.request("GET", f.base, nil, false)
			if w.Code != 200 || !strings.Contains(w.Body.String(), "Analyze Query Plan") || dbms == "mysql" && !strings.Contains(w.Body.String(), "implicit commit") {
				t.Fatalf("editor: %d %s", w.Code, w.Body.String())
			}
			if w := f.request("POST", f.base+"/preview", url.Values{"sql_query": {" "}}, true); w.Code != 422 {
				t.Fatalf("empty SQL: %d", w.Code)
			}
			query := "CREATE TABLE " + f.table + "(id int); INSERT INTO " + f.table + " VALUES(1)"
			w = f.request("POST", f.base+"/preview", url.Values{"sql_query": {query}}, true)
			if w.Code != 200 || !strings.Contains(w.Body.String(), "Pratinjau SQL") || !strings.Contains(w.Body.String(), "execution plan tabular") {
				t.Fatalf("preview: %d %s", w.Code, w.Body.String())
			}
			history, err := ListHistory(context.Background(), f.db, f.connection.ID)
			if err != nil || len(history) != 0 {
				t.Fatal("preview executed/persisted SQL")
			}
			for _, action := range []string{"/preview", "/execute", "/missing/retry", "/missing/reconcile"} {
				if w := f.request("POST", f.base+action, url.Values{"sql_query": {query}}, false); w.Code != 403 {
					t.Fatalf("missing CSRF %s: %d", action, w.Code)
				}
			}
			w = f.request("POST", f.base+"/execute", url.Values{"sql_query": {query}}, true)
			if w.Code != 303 {
				t.Fatalf("execute: %d %s", w.Code, w.Body.String())
			}
			w = f.request("GET", w.Header().Get("Location"), nil, false)
			if w.Code != 200 || !strings.Contains(w.Body.String(), "SUCCESS") || !strings.Contains(w.Body.String(), query) {
				t.Fatalf("detail: %d %s", w.Code, w.Body.String())
			}
			if w := f.request("POST", f.base+"/execute", url.Values{"sql_query": {strings.Repeat("x", 30*1024)}}, true); w.Code != 413 {
				t.Fatalf("oversize: %d", w.Code)
			}
		})
	}
}
