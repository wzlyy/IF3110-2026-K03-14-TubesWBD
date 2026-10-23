//go:build integration

package database

import (
	"context"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/admin"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
)

// Render the actual layout and page templates, including html/template escaping.
type connectionPageRenderer struct{ t *template.Template }

func (p connectionPageRenderer) ExecuteTemplate(w io.Writer, _ string, data any) error {
	return p.t.ExecuteTemplate(w, "layout.html", data)
}

func TestConnectionAdminHTTP(t *testing.T) {
	for _, dbms := range []string{"postgres", "mysql"} {
		t.Run(dbms, func(t *testing.T) { testConnectionAdminHTTP(t, dbms) })
	}
}

func testConnectionAdminHTTP(t *testing.T, dbms string) {
	store, manager := connectionFixture(t)
	page, err := template.ParseFiles("../../web/templates/layout.html", "../../web/templates/admin/connections.html")
	if err != nil {
		t.Fatal(err)
	}
	h := &ConnectionAdminHandler{Store: store, Manager: manager, Templates: connectionPageRenderer{page}}
	mux := http.NewServeMux()
	protect := func(fn http.HandlerFunc) http.Handler {
		return auth.RequireAuth(store.db, false)(auth.CSRFProtectionMiddleware(admin.RequireAdmin(fn)))
	}
	mux.Handle("GET /admin/connections", protect(h.ListConnections))
	mux.Handle("POST /admin/connections", protect(h.CreateConnection))
	mux.Handle("GET /admin/connections/{id}/edit", protect(h.ShowEdit))
	mux.Handle("POST /admin/connections/{id}/edit", protect(h.EditConnection))
	mux.Handle("POST /admin/connections/{id}/delete", protect(h.DeleteConnection))
	mux.Handle("POST /admin/connections/{id}/test", protect(h.TestConnection))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	session := func(isAdmin bool) *auth.Session {
		id := uuid.NewString()
		_, err := store.db.ExecContext(context.Background(), "INSERT INTO users(id,username,password_hash,is_platform_admin) VALUES($1,$2,'test-only',$3)", id, id, isAdmin)
		if err != nil {
			t.Fatal(err)
		}
		s, err := auth.CreateSession(context.Background(), store.db, id)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	adminSession, userSession := session(true), session(false)
	request := func(method, path string, values url.Values, s *auth.Session) (int, string, string) {
		t.Helper()
		var body io.Reader
		if values != nil {
			body = strings.NewReader(values.Encode())
		}
		r, err := http.NewRequest(method, srv.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		if values != nil {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if s != nil {
			r.AddCookie(&http.Cookie{Name: auth.CookieSessionName, Value: s.ID})
		}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(data), resp.Header.Get("Location")
	}
	for _, route := range []struct{ method, path string }{
		{"GET", "/admin/connections"}, {"POST", "/admin/connections"}, {"GET", "/admin/connections/missing/edit"},
		{"POST", "/admin/connections/missing/edit"}, {"POST", "/admin/connections/missing/delete"}, {"POST", "/admin/connections/missing/test"},
	} {
		status, _, location := request(route.method, route.path, nil, nil)
		if status != http.StatusSeeOther || location != "/login" {
			t.Fatalf("guest %s %s: %d %s", route.method, route.path, status, location)
		}
		status, _, _ = request(route.method, route.path, url.Values{"csrf_token": {userSession.CSRFToken}}, userSession)
		if status != http.StatusForbidden {
			t.Fatalf("non-admin %s %s: %d", route.method, route.path, status)
		}
		if route.method == "POST" {
			for _, token := range []string{"", "incorrect"} {
				status, _, _ = request(route.method, route.path, url.Values{"csrf_token": {token}}, adminSession)
				if status != http.StatusForbidden {
					t.Fatalf("CSRF %s: %d", route.path, status)
				}
			}
		}
	}
	status, body, _ := request("GET", "/admin/connections", nil, adminSession)
	if status != 200 || !strings.Contains(body, "Belum ada koneksi database") || !strings.Contains(body, "/static/css/database.css") {
		t.Fatalf("empty SSR: %d %s", status, body)
	}
	c, password := targetFixture()
	if dbms == "mysql" {
		if os.Getenv("TARGET_MYSQL_HOST") == "" {
			t.Skip("dedicated MySQL test environment not configured")
		}
		port, err := strconv.Atoi(os.Getenv("TARGET_MYSQL_PORT"))
		if err != nil {
			t.Fatal(err)
		}
		c = Connection{DBMS: "mysql", Host: os.Getenv("TARGET_MYSQL_HOST"), Port: port, DBName: os.Getenv("TARGET_MYSQL_NAME"), Username: os.Getenv("TARGET_MYSQL_USER")}
		password = os.Getenv("TARGET_MYSQL_PASSWORD")
	}
	values := url.Values{"csrf_token": {adminSession.CSRFToken}, "name": {"<script>alert('test')</script>"}, "dbms": {c.DBMS}, "host": {c.Host}, "port": {fmt.Sprint(c.Port)}, "db_name": {c.DBName}, "username": {c.Username}, "password": {password}}
	status, _, _ = request("POST", "/admin/connections", values, adminSession)
	if status != 303 {
		t.Fatalf("create HTTP: %d", status)
	}
	connections, err := store.List(context.Background())
	if err != nil || len(connections) != 1 {
		t.Fatalf("create metadata: %v %+v", err, connections)
	}
	id := connections[0].ID
	status, body, _ = request("GET", "/admin/connections/"+id+"/edit", nil, adminSession)
	passwordInput := regexp.MustCompile(`<input id="connection-password"[^>]*>`).FindString(body)
	if status != 200 || !strings.Contains(body, "Kosongkan untuk mempertahankan") || strings.Contains(body, "<script>alert") || !strings.Contains(body, "&lt;script&gt;") || passwordInput == "" || strings.Contains(passwordInput, "value=") {
		t.Fatalf("edit SSR leaked password or failed escaping: %d", status)
	}
	if !strings.Contains(body, "Semua hak akses group dan riwayat migration") {
		t.Fatal("missing cascade confirmation")
	}
	values.Set("name", "Edited via HTTP")
	values.Set("password", "")
	status, _, _ = request("POST", "/admin/connections/"+id+"/edit", values, adminSession)
	if status != 303 {
		t.Fatalf("edit HTTP: %d", status)
	}
	status, _, location := request("POST", "/admin/connections/"+id+"/test", url.Values{"csrf_token": {adminSession.CSRFToken}}, adminSession)
	if status != 303 || !strings.Contains(location, "success=") {
		t.Fatalf("test HTTP: %d %s", status, location)
	}
	values.Set("password", "wrong-http-credential")
	status, _, _ = request("POST", "/admin/connections/"+id+"/edit", values, adminSession)
	if status != 303 {
		t.Fatalf("password replacement HTTP: %d", status)
	}
	status, _, location = request("POST", "/admin/connections/"+id+"/test", url.Values{"csrf_token": {adminSession.CSRFToken}}, adminSession)
	if status != 303 || !strings.Contains(location, "error=") || strings.Contains(location, "wrong-http-credential") || strings.Contains(location, "SQLSTATE") {
		t.Fatalf("unsafe failed HTTP test: %d %s", status, location)
	}
	values.Set("port", "0")
	values.Set("password", "NeverRenderThisCredential")
	status, body, _ = request("POST", "/admin/connections/"+id+"/edit", values, adminSession)
	if status != 422 || !strings.Contains(body, "Edited via HTTP") || strings.Contains(body, "NeverRenderThisCredential") {
		t.Fatalf("invalid edit SSR: %d", status)
	}
	values.Set("port", fmt.Sprint(c.Port))
	// SELECT remains available, while both valid writes fail inside PostgreSQL.
	if _, err := store.db.Exec(`CREATE FUNCTION reject_connection_write() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'driver-secret-sentinel password=private SQLSTATE'; END $$;
		CREATE TRIGGER reject_connection_write BEFORE INSERT OR UPDATE ON database_connections
		FOR EACH ROW EXECUTE FUNCTION reject_connection_write()`); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/admin/connections", "/admin/connections/" + id + "/edit"} {
		status, body, _ = request("POST", path, values, adminSession)
		if status != http.StatusInternalServerError {
			t.Errorf("valid write failure %s: got %d want 500", path, status)
		}
		for _, secret := range []string{"driver-secret-sentinel", "password=private", "SQLSTATE", "NeverRenderThisCredential"} {
			if strings.Contains(body, secret) {
				t.Errorf("write failure leaked %q", secret)
			}
		}
	}
	if _, err := store.db.Exec("DROP TRIGGER reject_connection_write ON database_connections; DROP FUNCTION reject_connection_write()"); err != nil {
		t.Fatal(err)
	}
	// Real FK cascades remove grants and migration history when an admin deletes.
	groupID := uuid.NewString()
	if _, err := store.db.ExecContext(context.Background(), "INSERT INTO groups(id,name) VALUES($1,'Cascade group')", groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(context.Background(), "INSERT INTO group_database_grants(group_id,database_id,access_level) VALUES($1,$2,'VIEW_ONLY')", groupID, id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(context.Background(), "INSERT INTO migrations(id,database_id,version,sql_query,status) VALUES($1,$2,1,'SELECT 1','SUCCESS')", uuid.NewString(), id); err != nil {
		t.Fatal(err)
	}
	status, _, _ = request("POST", "/admin/connections/"+id+"/delete", url.Values{"csrf_token": {adminSession.CSRFToken}}, adminSession)
	if status != 303 {
		t.Fatalf("delete HTTP: %d", status)
	}
	for _, table := range []string{"database_connections", "group_database_grants", "migrations"} {
		var count int
		if err := store.db.QueryRowContext(context.Background(), "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("cascade %s: count=%d error=%v", table, count, err)
		}
	}
	status, _, _ = request("GET", "/admin/connections/"+id+"/edit", nil, adminSession)
	if status != 404 {
		t.Fatalf("missing edit: %d", status)
	}
}
