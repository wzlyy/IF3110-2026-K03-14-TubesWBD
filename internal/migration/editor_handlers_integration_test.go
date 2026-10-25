//go:build integration

package migration

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"html/template"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
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
	for _, page := range []string{"editor", "history", "detail", "diff"} {
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

func TestGeneratedSQLToEditorFlow(t *testing.T) {
	for _, dbms := range []string{"postgres", "mysql"} {
		t.Run(dbms, func(t *testing.T) {
			f := migrationHTTPFixture(t, dbms, "VIEW_EDIT")
			diffURL := "/databases/" + f.connection.ID + "/diff"
			// Start with a prior successful migration, so the internal receipt table exists.
			if prior := f.execute(t, "CREATE TABLE "+f.table+"(id integer NOT NULL)"); prior.Status != Success {
				t.Fatalf("prior migration: %+v", prior)
			}
			newTable := f.table + "_diff"
			t.Cleanup(func() { _, _ = f.target.Exec("DROP TABLE IF EXISTS " + newTable) })
			catalog, err := database.LoadCatalog(context.Background(), f.target, dbms, f.connection.DBName)
			if err != nil {
				t.Fatal(err)
			}
			// Preserve every application table: only the internal receipt is intentionally omitted.
			desired := desiredApplicationCatalog(catalog)
			schema := "public"
			if dbms == "mysql" {
				schema = f.connection.DBName
			}
			desired.Tables = append(desired.Tables, TableDef{
				Schema: schema, Name: newTable,
				Columns:     []ColumnDef{{Name: "id", DataType: "integer", Nullable: false}},
				PrimaryKey:  []string{"id"},
				Constraints: []ConstraintDef{{Name: newTable + "_positive", Type: "CHECK", Columns: []string{"id"}, Definition: "CHECK (id > 0)"}},
				Indexes:     []IndexDef{{Name: newTable + "_idx", Columns: []string{"id"}}},
			})
			targetJSON, err := json.Marshal(desired)
			if err != nil {
				t.Fatal(err)
			}

			// 1. GET diff page
			w := f.request("GET", diffURL, nil, false)
			if w.Code != 200 || !strings.Contains(w.Body.String(), "Bandingkan Skema") {
				t.Fatalf("GET diff failed: %d %s", w.Code, w.Body.String())
			}

			// 2. POST diff with target schema JSON
			w = f.request("POST", diffURL, url.Values{"target_schema_json": {string(targetJSON)}}, true)
			if w.Code != 200 || !strings.Contains(w.Body.String(), "Bawa SQL ke Editor Migration") {
				t.Fatalf("POST diff failed: %d %s", w.Code, w.Body.String())
			}

			// 3. Bring SQL to Editor via preview
			candidate := regexp.MustCompile(`<input\s+type="hidden"\s+name="sql_query"\s+value="([^"]*)"`).FindStringSubmatch(w.Body.String())
			if len(candidate) != 2 {
				t.Fatal("generated SQL transfer field missing")
			}
			candSQL := html.UnescapeString(candidate[1])
			if strings.Contains(strings.ToLower(candSQL), "_dbplatform_migration_receipts") {
				t.Fatalf("generated candidate must preserve internal receipts: %s", candSQL)
			}
			w = f.request("POST", f.base+"/preview", url.Values{"sql_query": {candSQL}}, true)
			if w.Code != 200 || !strings.Contains(w.Body.String(), "Pratinjau SQL") || !strings.Contains(w.Body.String(), f.table+"_diff") {
				t.Fatalf("Preview generated SQL failed: %d %s", w.Code, w.Body.String())
			}
			before, err := ListHistory(context.Background(), f.db, f.connection.ID)
			if err != nil || len(before) != 1 {
				t.Fatalf("preview must not create an attempt: %+v %v", before, err)
			}
			w = f.request("POST", f.base+"/execute", url.Values{"sql_query": {candSQL}}, true)
			if w.Code != 303 {
				t.Fatalf("execute generated SQL: %d %s", w.Code, w.Body.String())
			}
			detail := f.request("GET", w.Header().Get("Location"), nil, false)
			if detail.Code != 200 || !strings.Contains(detail.Body.String(), "SUCCESS") || !strings.Contains(html.UnescapeString(detail.Body.String()), candSQL) {
				t.Fatalf("generated attempt detail: %d %s", detail.Code, detail.Body.String())
			}
			history, err := ListHistory(context.Background(), f.db, f.connection.ID)
			if err != nil || len(history) != 2 || history[0].Status != Success || history[0].Version != 2 || history[0].SQL != candSQL {
				t.Fatalf("generated attempt history: %+v %v", history, err)
			}
			fresh, err := database.LoadCatalog(context.Background(), f.target, dbms, f.connection.DBName)
			if err != nil {
				t.Fatal(err)
			}
			created, exists := fresh.FindTable(schema, newTable)
			if !exists {
				t.Fatal("generated table not committed")
			}
			var check, primary, index bool
			for _, c := range created.Constraints {
				check = check || c.Name == newTable+"_positive" && c.Type == "CHECK"
				primary = primary || c.Type == "PRIMARY KEY" && len(c.Columns) == 1 && c.Columns[0] == "id"
			}
			for _, i := range created.Indexes {
				index = index || i.Name == newTable+"_idx" && !i.Unique && len(i.Columns) == 1 && i.Columns[0] == "id"
			}
			if !check || !primary || !index {
				t.Fatalf("generated table lost CHECK/PK/index: %+v", created)
			}
			if _, err := f.target.Exec("INSERT INTO " + newTable + " VALUES(-1)"); err == nil {
				t.Fatal("generated CHECK constraint did not reject invalid data")
			}

			// 4. View Only permission check: View Only can view diff, but cannot preview/edit
			fReadOnly := migrationHTTPFixture(t, dbms, "VIEW_ONLY")
			diffURLRO := "/databases/" + fReadOnly.connection.ID + "/diff"
			wRO := fReadOnly.request("GET", diffURLRO, nil, false)
			if wRO.Code != 200 {
				t.Fatalf("View Only expected 200 on GET diff, got: %d", wRO.Code)
			}
			wROPreview := fReadOnly.request("POST", fReadOnly.base+"/preview", url.Values{"sql_query": {candSQL}}, true)
			if wROPreview.Code != 403 {
				t.Fatalf("View Only expected 403 on POST preview, got: %d", wROPreview.Code)
			}
		})
	}
}

// Snapshot user objects without feeding receipt metadata back into the generator.
func desiredApplicationCatalog(catalog *database.Catalog) *TargetSchemaDefinition {
	desired := &TargetSchemaDefinition{DBMS: catalog.DBMS}
	for _, schema := range catalog.Schemas {
		for _, table := range schema.Tables {
			if strings.EqualFold(table.Name, "_dbplatform_migration_receipts") {
				continue
			}
			tbl := TableDef{Schema: table.Schema, Name: table.Name}
			for _, c := range table.Columns {
				tbl.Columns = append(tbl.Columns, ColumnDef{Name: c.Name, DataType: c.DataType, Nullable: c.Nullable, Default: c.Default})
			}
			constraintNames := map[string]bool{}
			for _, c := range table.Constraints {
				tbl.Constraints = append(tbl.Constraints, ConstraintDef{Name: c.Name, Type: c.Type, Columns: c.Columns, Definition: c.Definition})
				constraintNames[c.Name] = true
			}
			for _, i := range table.Indexes {
				if !i.Primary && !constraintNames[i.Name] {
					tbl.Indexes = append(tbl.Indexes, IndexDef{Name: i.Name, Columns: i.Columns, Unique: i.Unique})
				}
			}
			desired.Tables = append(desired.Tables, tbl)
		}
	}
	return desired
}

func TestDiffAcceptsFileWithoutTextarea(t *testing.T) {
	f := migrationHTTPFixture(t, "postgres", "VIEW_EDIT")
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("csrf_token", f.session.CSRFToken); err != nil {
		t.Fatal(err)
	}
	file, err := form.CreateFormFile("schema_file", "schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, `{"tables":[{"name":"`+f.table+`","columns":[{"name":"id","type":"integer"}]}]}`); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/databases/"+f.connection.ID+"/diff", &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	r.AddCookie(&http.Cookie{Name: auth.CookieSessionName, Value: f.session.ID})
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Bawa SQL ke Editor Migration") {
		t.Fatalf("file-only schema input: %d %s", w.Code, w.Body.String())
	}
	if got := f.request("POST", "/databases/"+f.connection.ID+"/diff", nil, true); got.Code != 422 {
		t.Fatalf("both empty must still be rejected: %d", got.Code)
	}
}

func TestDiffRejectsAmbiguousSources(t *testing.T) {
	f := migrationHTTPFixture(t, "postgres", "VIEW_EDIT")
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	_ = form.WriteField("csrf_token", f.session.CSRFToken)
	_ = form.WriteField("target_schema_json", `{"tables":[{"name":"text_source","columns":[{"name":"id","type":"integer"}]}]}`)
	file, err := form.CreateFormFile("schema_file", "schema.json")
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.WriteString(file, `{"tables":[{"name":"file_source","columns":[{"name":"id","type":"integer"}]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if err = form.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/databases/"+f.connection.ID+"/diff", &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	r.AddCookie(&http.Cookie{Name: auth.CookieSessionName, Value: f.session.ID})
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != 422 || !strings.Contains(w.Body.String(), "Pilih satu sumber") {
		t.Fatalf("ambiguous sources must be rejected explicitly: %d", w.Code)
	}
}
