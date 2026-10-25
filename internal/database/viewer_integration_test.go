//go:build integration

package database

import (
	"context"
	"database/sql"
	"fmt"
	"html"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/access"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
)

func viewerFixture(t *testing.T) (*ConnectionStore, *TargetManager, *Connection, *sql.DB, string, string) {
	t.Helper()
	store, manager := connectionFixture(t)
	ctx := context.Background()
	c, password := targetFixture()
	saved, err := store.Create(ctx, c, password)
	if err != nil {
		t.Fatal(err)
	}
	target, _, err := manager.Open(ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	schema := "viewer_" + strings.ReplaceAll(uuid.NewString(), "-", "") + ` "&/雪`
	other := schema + "_empty"
	q, _ := QuoteIdentifier("postgres", schema)
	qo, _ := QuoteIdentifier("postgres", other)
	if _, err = target.ExecContext(ctx, "CREATE SCHEMA "+q+"; CREATE SCHEMA "+qo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, err := sql.Open("pgx", postgresDSN(c, password))
		if err != nil {
			t.Error(err)
			return
		}
		defer cleanup.Close()
		if _, err := cleanup.ExecContext(context.Background(), "DROP SCHEMA "+q+" CASCADE; DROP SCHEMA "+qo+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	exec := func(query string) {
		t.Helper()
		if _, err := target.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TABLE ` + q + `.parent(a integer,b integer,PRIMARY KEY(a,b));
 CREATE TABLE ` + q + `.details(id integer PRIMARY KEY,label text DEFAULT 'hello',a integer,b integer,CONSTRAINT positive_id CHECK(id>0),CONSTRAINT pair_fk FOREIGN KEY(a,b) REFERENCES ` + q + `.parent(a,b),CONSTRAINT unique_label UNIQUE(label));
 CREATE INDEX details_expr ON ` + q + `.details USING btree (lower(label),id DESC) INCLUDE(a) WHERE id>2;
 CREATE VIEW ` + q + `.not_a_table AS SELECT 1 AS x;
 CREATE TABLE ` + q + `.no_key(value text);
 CREATE TABLE ` + q + `.zero_columns();
 CREATE DOMAIN ` + q + `.required_text AS text NOT NULL;
 CREATE TABLE ` + q + `.domain_values(required_value ` + q + `.required_text, optional_value text);
 CREATE TABLE ` + q + `.scalar_values(id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY, doubled integer GENERATED ALWAYS AS (id*2) STORED,stamp timestamptz,payload bytea);
 INSERT INTO ` + q + `.scalar_values(stamp,payload) VALUES ('2026-10-22T12:13:14Z',decode('00ff','hex'));`)
	for _, count := range []int{0, 50, 51, 101} {
		exec(fmt.Sprintf(`CREATE TABLE %s.rows_%d(id integer PRIMARY KEY,value text); INSERT INTO %s.rows_%d SELECT g,CASE WHEN g=1 THEN NULL WHEN g=2 THEN '' WHEN g=3 THEN '<script>alert(1)</script>' ELSE 'row-'||g END FROM generate_series(1,%d) g`, q, count, q, count, count))
	}
	table := `quoted "&/ + 雪`
	qt, _ := QuoteIdentifier("postgres", table)
	exec(`CREATE TABLE ` + q + `.` + qt + `(id integer PRIMARY KEY,value text); INSERT INTO ` + q + `.` + qt + ` SELECT g,repeat('long text ',500) FROM generate_series(1,51) g`)
	return store, manager, saved, target, schema, other
}

func TestPostgresCatalogAndData(t *testing.T) {
	_, _, c, target, schema, empty := viewerFixture(t)
	ctx := context.Background()
	catalog, err := LoadCatalog(ctx, target, "postgres", c.DBName)
	if err != nil {
		t.Fatal(err)
	}
	var emptyFound bool
	for _, s := range catalog.Schemas {
		if s.Name == empty {
			emptyFound = true
			if len(s.Tables) != 0 {
				t.Fatal("empty schema acquired tables")
			}
		}
		if s.Name == "information_schema" || strings.HasPrefix(s.Name, "pg_") {
			t.Fatalf("system schema leaked: %s", s.Name)
		}
	}
	if !emptyFound {
		t.Fatal("empty schema omitted")
	}
	if _, ok := catalog.FindTable(schema, "not_a_table"); ok {
		t.Fatal("view exposed as table")
	}
	details, ok := catalog.FindTable(schema, "details")
	if !ok {
		t.Fatal("details missing")
	}
	if len(details.Columns) != 4 || details.Columns[0].Nullable || details.Columns[1].Default == nil || !strings.Contains(*details.Columns[1].Default, "hello") {
		t.Fatalf("columns/defaults: %+v", details.Columns)
	}
	domainTable, ok := catalog.FindTable(schema, "domain_values")
	if !ok || domainTable.Columns[0].Nullable || !domainTable.Columns[1].Nullable {
		t.Fatalf("effective domain nullability: %+v", domainTable)
	}
	kinds := map[string]bool{}
	for _, con := range details.Constraints {
		kinds[con.Type] = true
		if con.Definition == "" {
			t.Fatal("missing definition")
		}
		if con.Type == "FOREIGN KEY" && (len(con.Columns) != 2 || con.Columns[0] != "a" || !strings.Contains(con.Definition, "REFERENCES")) {
			t.Fatalf("foreign key: %+v", con)
		}
	}
	for _, kind := range []string{"PRIMARY KEY", "FOREIGN KEY", "UNIQUE", "CHECK"} {
		if !kinds[kind] {
			t.Fatalf("constraint %s missing", kind)
		}
	}
	exprFound := false
	for _, index := range details.Indexes {
		if index.Name == "details_expr" {
			exprFound = true
			if index.Type != "btree" || len(index.Columns) != 2 || !strings.Contains(index.Columns[0], "lower") || index.Columns[1] != "id" || !strings.Contains(index.Definition, "id DESC") || !strings.Contains(index.Definition, "INCLUDE") || !strings.Contains(index.Definition, "WHERE") {
				t.Fatalf("expression index: %+v", index)
			}
		}
	}
	if !exprFound {
		t.Fatal("expression index omitted")
	}
	parent, _ := catalog.FindTable(schema, "parent")
	if len(parent.Constraints) != 1 || len(parent.Constraints[0].Columns) != 2 {
		t.Fatalf("composite PK: %+v", parent)
	}
	for _, count := range []int{0, 50, 51, 101} {
		table, _ := catalog.FindTable(schema, fmt.Sprintf("rows_%d", count))
		for page := int64(1); page <= 3; page++ {
			got, err := ReadTableData(ctx, target, "postgres", *table, page)
			if err != nil {
				t.Fatal(err)
			}
			remaining := count - int(page-1)*50
			want := max(0, min(50, remaining))
			if len(got.Rows) != want || got.HasNext != (remaining > 50) || !got.OrderedByPrimaryKey {
				t.Fatalf("count%d/page%d: rows%d next%v", count, page, len(got.Rows), got.HasNext)
			}
			if page == 1 && count > 2 {
				if !got.Rows[0][1].Null || got.Rows[1][1].Null || got.Rows[1][1].Text != "" || got.Rows[2][1].Text != "<script>alert(1)</script>" {
					t.Fatalf("NULL/empty/HTML lost: %+v", got.Rows[:3])
				}
			}
			if want > 0 && got.Rows[0][0].Text != fmt.Sprint((page-1)*50+1) {
				t.Fatal("PK ordering incorrect")
			}
		}
	}
	noKey, _ := catalog.FindTable(schema, "no_key")
	got, err := ReadTableData(ctx, target, "postgres", *noKey, 1)
	if err != nil || got.OrderedByPrimaryKey {
		t.Fatalf("no-key fallback: %+v %v", got, err)
	}
	zero, _ := catalog.FindTable(schema, "zero_columns")
	if _, err := ReadTableData(ctx, target, "postgres", *zero, 1); err != nil {
		t.Fatalf("zero columns: %v", err)
	}
	scalars, _ := catalog.FindTable(schema, "scalar_values")
	if scalars.Columns[0].Identity != "a" || scalars.Columns[1].Generated != "s" || scalars.Columns[1].Default == nil {
		t.Fatalf("identity/generated metadata: %+v", scalars.Columns)
	}
	values, err := ReadTableData(ctx, target, "postgres", *scalars, 1)
	if err != nil || len(values.Rows) != 1 || values.Rows[0][3].Text != `\x00ff` {
		t.Fatalf("driver scalar values: %+v %v", values, err)
	}
	stamp, err := time.Parse(time.RFC3339Nano, values.Rows[0][2].Text)
	if err != nil || !stamp.Equal(time.Date(2026, 10, 22, 12, 13, 14, 0, time.UTC)) {
		t.Fatalf("timestamp instant changed: %q %v", values.Rows[0][2].Text, err)
	}
	// Same table name in another schema must resolve to that exact schema.
	qe, _ := QuoteIdentifier("postgres", empty)
	if _, err := target.ExecContext(ctx, "CREATE TABLE "+qe+".details(marker text); INSERT INTO "+qe+".details VALUES('other schema')"); err != nil {
		t.Fatal(err)
	}
	fresh, err := LoadCatalog(ctx, target, "postgres", c.DBName)
	if err != nil {
		t.Fatal(err)
	}
	otherDetails, ok := fresh.FindTable(empty, "details")
	if !ok {
		t.Fatal("second schema table missing")
	}
	otherData, err := ReadTableData(ctx, target, "postgres", *otherDetails, 1)
	if err != nil || otherData.Rows[0][0].Text != "other schema" {
		t.Fatalf("cross-schema selection: %+v %v", otherData, err)
	}
	// A real conflicting lock proves caller deadlines bound target reads.
	lock, err := target.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	qs, _ := QuoteIdentifier("postgres", schema)
	if _, err = lock.ExecContext(ctx, "LOCK TABLE "+qs+".details IN ACCESS EXCLUSIVE MODE"); err != nil {
		lock.Rollback()
		t.Fatal(err)
	}
	short, stop := context.WithTimeout(ctx, 150*time.Millisecond)
	startedRead := time.Now()
	_, readErr := ReadTableData(short, target, "postgres", *details, 1)
	stop()
	lock.Rollback()
	if readErr == nil || time.Since(startedRead) > time.Second {
		t.Fatalf("target read ignored deadline: %v", readErr)
	}
	if _, err = ReadTableData(ctx, target, "postgres", Table{Schema: schema, Name: "details"}, 1); err != ErrTableNotFound {
		t.Fatalf("unvalidated table: %v", err)
	}
	if _, err = LoadCatalog(ctx, target, "sqlite", c.DBName); err != ErrUnsupportedDBMS {
		t.Fatal("unsupported catalog accepted")
	}
	stale, _ := catalog.FindTable(schema, "rows_0")
	q, _ := QuoteIdentifier("postgres", schema)
	if _, err = target.ExecContext(ctx, "DROP TABLE "+q+".rows_0"); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadTableData(ctx, target, "postgres", *stale, 1); err != ErrTableNotFound {
		t.Fatalf("dropped table: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	started := time.Now()
	if _, err = LoadCatalog(canceled, target, "postgres", c.DBName); err == nil || time.Since(started) > time.Second {
		t.Fatalf("cancellation: %v", err)
	}
}

type viewerPageRenderer map[string]*template.Template

func (p viewerPageRenderer) ExecuteTemplate(w io.Writer, name string, data any) error {
	return p[name].ExecuteTemplate(w, "layout.html", data)
}

func TestViewerHTTPAuthorizationAndSSR(t *testing.T) {
	store, manager, c, _, schema, _ := viewerFixture(t)
	pages := viewerPageRenderer{}
	for _, name := range []string{"list", "schema", "data"} {
		page, err := template.ParseFiles("../../web/templates/layout.html", "../../web/templates/database/"+name+".html")
		if err != nil {
			t.Fatal(err)
		}
		pages["database-"+name+".html"] = page
	}
	h := &ViewerHandler{Store: store, Manager: manager, Templates: pages}
	mux := http.NewServeMux()
	requireAuth := auth.RequireAuth(store.db, false)
	protect := access.RequireDatabaseAccess(store.db, access.AccessViewOnly)
	mux.Handle("GET /databases", requireAuth(http.HandlerFunc(h.ListDatabases)))
	mux.Handle("GET /databases/{database_id}/schema", requireAuth(protect(http.HandlerFunc(h.ShowSchema))))
	mux.Handle("GET /databases/{database_id}/data", requireAuth(protect(http.HandlerFunc(h.ShowData))))
	session := func(admin bool) *auth.Session {
		id := uuid.NewString()
		if _, err := store.db.Exec("INSERT INTO users(id,username,password_hash,is_platform_admin) VALUES($1,$2,'test-only',$3)", id, id, admin); err != nil {
			t.Fatal(err)
		}
		s, err := auth.CreateSession(context.Background(), store.db, id)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	admin, user := session(true), session(false)
	request := func(path string, s *auth.Session) (int, string) {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		if s != nil {
			r.AddCookie(&http.Cookie{Name: auth.CookieSessionName, Value: s.ID})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code == 200 && w.Header().Get("Cache-Control") != "no-store" {
			t.Error("missing no-store")
		}
		return w.Code, w.Body.String()
	}
	for _, s := range []*auth.Session{admin, user} {
		status, body := request("/databases", s)
		if status != 200 || strings.Contains(body, c.Name) || !strings.Contains(body, "Belum ada database yang dapat diakses") {
			t.Fatalf("ungranted list: %d %s", status, body)
		}
		if s == admin && !strings.Contains(body, "Kelola hak akses group") {
			t.Fatal("admin grant recovery missing")
		}
		for _, view := range []string{"schema", "data"} {
			if status, _ := request("/databases/"+c.ID+"/"+view, s); status != 403 {
				t.Fatalf("ungranted direct %s: %d", view, status)
			}
		}
	}
	if status, _ := request("/databases/"+c.ID+"/schema", nil); status != 303 {
		t.Fatalf("guest status %d", status)
	}
	group := uuid.NewString()
	if _, err := store.db.Exec("INSERT INTO groups(id,name) VALUES($1,'Viewer group')", group); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("INSERT INTO user_groups(user_id,group_id) VALUES($1,$2)", user.UserID, group); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("INSERT INTO group_database_grants(group_id,database_id,access_level) VALUES($1,$2,'VIEW_ONLY')", group, c.ID); err != nil {
		t.Fatal(err)
	}
	status, body := request("/databases", user)
	if status != 200 || !strings.Contains(body, c.Name) || !strings.Contains(body, "Lihat saja") || !strings.Contains(body, "PostgreSQL") {
		t.Fatalf("granted list: %d", status)
	}
	schemaPath := tableURL(c.ID, "schema", schema, "details", 1)
	status, body = request(schemaPath, user)
	if status != 200 || !strings.Contains(body, "FOREIGN KEY") || !strings.Contains(body, "details_expr") || !strings.Contains(body, "hello") {
		t.Fatalf("schema SSR: %d %s", status, body)
	}
	status, body = request(tableURL(c.ID, "data", schema, "rows_51", 1), user)
	if status != 200 || !strings.Contains(body, "database-null\">NULL") || !strings.Contains(body, "(string kosong)") || strings.Contains(body, "<script>alert(1)</script>") || !strings.Contains(body, "&lt;script&gt;") || !strings.Contains(body, `rel="next"`) || strings.Count(body, "<tr>") != 51 {
		t.Fatalf("data SSR: %d %s", status, body)
	}
	status, body = request(tableURL(c.ID, "data", schema, `quoted "&/ + 雪`, 1), user)
	if status != 200 || !strings.Contains(body, `aria-current="page"`) || !strings.Contains(body, "%26") || !strings.Contains(body, "%2F") || !strings.Contains(body, "page=2") {
		t.Fatalf("quoted URL SSR: %d %s", status, body)
	}
	// Follow the actual prominent header action, not a reconstructed helper URL.
	quotedTable := `quoted "&/ + 雪`
	headerURL := func(t *testing.T, body, label string) string {
		t.Helper()
		for _, match := range regexp.MustCompile(`<a class="btn btn-(?:primary|secondary)" href="([^"]*)">([^<]*)</a>`).FindAllStringSubmatch(body, -1) {
			if strings.Join(strings.Fields(html.UnescapeString(match[2])), " ") == label {
				return html.UnescapeString(match[1])
			}
		}
		t.Fatalf("missing header action %q", label)
		return ""
	}
	for _, tc := range []struct{ source, label, target string }{
		{"schema", "Lihat data", "data"}, {"data", "Lihat skema", "schema"},
	} {
		t.Run("header_"+tc.source+"_to_"+tc.target, func(t *testing.T) {
			status, sourceBody := request(tableURL(c.ID, tc.source, schema, quotedTable, 2), user)
			if status != 200 {
				t.Fatalf("source status %d", status)
			}
			switchPath := headerURL(t, sourceBody, tc.label)
			parsed, err := url.Parse(switchPath)
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Query().Get("schema") != schema || parsed.Query().Get("table") != quotedTable {
				t.Errorf("header lost selected relation: %s", switchPath)
			}
			if tc.target == "data" && parsed.Query().Get("page") != "1" {
				t.Errorf("data header did not reset page1: %s", switchPath)
			}
			status, switched := request(switchPath, user)
			if status != 200 {
				t.Fatalf("switched status %d", status)
			}
			selectedText := schema + "." + quotedTable
			decoded := html.UnescapeString(switched)
			if !regexp.MustCompile(`aria-current="page"\s*>` + regexp.QuoteMeta(selectedText) + `</a>`).MatchString(decoded) {
				t.Errorf("header switched away from selected quoted table: expected %q, actual %q", selectedText, regexp.MustCompile(`aria-current="page"[^<]*</a>`).FindString(decoded))
			}
			if tc.target == "data" {
				if !strings.Contains(switched, "long text long text") || strings.Count(switched, "<tr>") != 51 || !strings.Contains(switched, "Halaman 1 · 50 baris") {
					t.Error("header did not open selected table data page1")
				}
			} else if strings.Count(switched, "<h3>") != 1 || !strings.Contains(decoded, "<h3>"+selectedText+"</h3>") {
				t.Errorf("header opened schema overview instead of selected table: actual %q", regexp.MustCompile(`<h3>[^<]*</h3>`).FindAllString(decoded, -1))
			}
		})
	}
	status, body = request("/databases/"+c.ID+"/schema", user)
	if status != 200 || headerURL(t, body, "Lihat data") != "/databases/"+c.ID+"/data" {
		t.Fatal("unselected schema overview should use generic data URL")
	}
	for _, tc := range []struct {
		query string
		want  int
	}{{"page=0", 400}, {"page=999999999999999999999999", 400}, {"schema=unknown&table=none", 404}, {"schema=" + url.QueryEscape(schema) + "&table=" + url.QueryEscape(`details"; DROP TABLE details;--`), 404}, {"schema=" + url.QueryEscape(schema), 404}} {
		status, body = request("/databases/"+c.ID+"/data?"+tc.query, user)
		if status != tc.want || !strings.Contains(body, "<!DOCTYPE html>") || strings.Contains(body, "SQLSTATE") {
			t.Fatalf("invalid %s: %d %s", tc.query, status, body)
		}
	}
	status, body = request(tableURL(c.ID, "data", schema, "rows_0", 1), user)
	if status != 200 || !strings.Contains(body, "Tidak ada baris") {
		t.Fatalf("empty table SSR %d", status)
	}
	status, body = request(tableURL(c.ID, "data", schema, "no_key", 1), user)
	if status != 200 || !strings.Contains(body, "Urutan baris dapat berubah") {
		t.Fatalf("fallback SSR %d", status)
	}
	// Updating the canonical grant is reflected on the very next request.
	if _, err := store.db.Exec("UPDATE group_database_grants SET access_level='VIEW_EDIT' WHERE group_id=$1", group); err != nil {
		t.Fatal(err)
	}
	status, body = request("/databases", user)
	if status != 200 || !strings.Contains(body, "Lihat &amp; edit") {
		t.Fatal("effective grant upgrade missing")
	}
	// Save a reachable-format but refused TCP endpoint; errors must stay sanitized.
	offline := *c
	offline.Host = "127.0.0.1"
	offline.Port = 1
	if err := store.Update(context.Background(), c.ID, offline, ""); err != nil {
		t.Fatal(err)
	}
	manager.Invalidate(c.ID)
	status, body = request("/databases/"+c.ID+"/schema", user)
	if status != 503 || !strings.Contains(body, "<!DOCTYPE html>") || strings.Contains(body, "127.0.0.1") || strings.Contains(body, "SQLSTATE") {
		t.Fatalf("offline SSR: %d %s", status, body)
	}
	if _, err := store.db.Exec("DELETE FROM group_database_grants WHERE group_id=$1", group); err != nil {
		t.Fatal(err)
	}
	if status, _ := request("/databases/"+c.ID+"/data", user); status != 403 {
		t.Fatal("revocation did not protect direct URL")
	}
	status, body = request("/databases", user)
	if status != 200 || strings.Contains(body, c.Name) {
		t.Fatal("revoked connection leaked into list")
	}
}
