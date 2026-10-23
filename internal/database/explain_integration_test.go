//go:build integration

package database

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/access"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
)

func TestExplainPlanSafeDefault(t *testing.T) {
	_, _, _, target, schema, _ := viewerFixture(t)
	q, _ := QuoteIdentifier("postgres", schema)
	ctx := context.Background()
	if _, err := target.Exec("CREATE TABLE " + q + ".explain_rows(id serial PRIMARY KEY, label text); INSERT INTO " + q + ".explain_rows(label) VALUES('before')"); err != nil {
		t.Fatal(err)
	}
	var beforeValue int64
	var beforeCalled bool
	if err := target.QueryRow("SELECT last_value,is_called FROM "+q+".explain_rows_id_seq").Scan(&beforeValue, &beforeCalled); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"SELECT * FROM " + q + ".explain_rows WHERE id=1",
		"INSERT INTO " + q + ".explain_rows(label) VALUES('planned')",
		"UPDATE " + q + ".explain_rows SET label='planned' WHERE id=1",
		"DELETE FROM " + q + ".explain_rows WHERE id=1",
	} {
		plan, err := RunExplain(ctx, target, "postgres", query, false)
		if err != nil || plan.Root == nil || plan.Analyzed || plan.Root.ActualTime != nil {
			t.Fatalf("safe default %q: %+v %v", query, plan, err)
		}
	}
	var count int
	var label string
	if err := target.QueryRow("SELECT count(*),min(label) FROM "+q+".explain_rows").Scan(&count, &label); err != nil || count != 1 || label != "before" {
		t.Fatalf("default altered rows: %d %q %v", count, label, err)
	}
	var afterValue int64
	var afterCalled bool
	if err := target.QueryRow("SELECT last_value,is_called FROM "+q+".explain_rows_id_seq").Scan(&afterValue, &afterCalled); err != nil || beforeValue != afterValue || beforeCalled != afterCalled {
		t.Fatalf("default advanced sequence: %d/%v -> %d/%v %v", beforeValue, beforeCalled, afterValue, afterCalled, err)
	}
}

func TestExplainAnalyzeRollbackProtection(t *testing.T) {
	_, _, _, target, schema, _ := viewerFixture(t)
	q, _ := QuoteIdentifier("postgres", schema)
	if _, err := target.Exec("CREATE TABLE " + q + ".explain_changes(id serial PRIMARY KEY, label text); INSERT INTO " + q + ".explain_changes(label) VALUES('before')"); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"INSERT INTO " + q + ".explain_changes(label) VALUES('rolled back')",
		"UPDATE " + q + ".explain_changes SET label='changed' WHERE id=1",
		"DELETE FROM " + q + ".explain_changes WHERE id=1",
	} {
		plan, err := RunExplain(context.Background(), target, "postgres", query, true)
		if err != nil || plan.Root == nil || !plan.Analyzed || plan.Root.ActualTime == nil {
			t.Fatalf("analyze %q: %+v %v", query, plan, err)
		}
		var count int
		var label string
		if err := target.QueryRow("SELECT count(*),min(label) FROM "+q+".explain_changes").Scan(&count, &label); err != nil || count != 1 || label != "before" {
			t.Fatalf("analyze persisted rows: %d %q %v", count, label, err)
		}
	}
	var last int64
	if err := target.QueryRow("SELECT last_value FROM " + q + ".explain_changes_id_seq").Scan(&last); err != nil || last < 2 {
		t.Fatalf("sequence caveat not reproduced: %d %v", last, err)
	}
}

func TestMySQLExplainJSONAndTree(t *testing.T) {
	_, _, saved, target, prefix := mysqlTargetFixture(t)
	qdb, _ := QuoteIdentifier("mysql", saved.DBName)
	qt, _ := QuoteIdentifier("mysql", prefix+"_plan")
	table := qdb + "." + qt
	if _, err := target.Exec("CREATE TABLE " + table + "(id INT AUTO_INCREMENT PRIMARY KEY, label VARCHAR(30), KEY label_index(label))"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = target.Exec("DROP TABLE IF EXISTS " + table) })
	if _, err := target.Exec("INSERT INTO " + table + "(label) VALUES('before')"); err != nil {
		t.Fatal(err)
	}
	autoIncrement := func() int64 {
		t.Helper()
		var next int64
		if err := target.QueryRow("SELECT AUTO_INCREMENT FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME=?", saved.DBName, prefix+"_plan").Scan(&next); err != nil {
			t.Fatal(err)
		}
		return next
	}
	beforeNext := autoIncrement()
	for _, query := range []string{
		"SELECT * FROM " + table + " WHERE label='before'",
		"INSERT INTO " + table + "(label) VALUES('planned')",
		"UPDATE " + table + " SET label='planned' WHERE id=1",
		"DELETE FROM " + table + " WHERE id=1",
	} {
		plan, err := RunExplain(context.Background(), target, "mysql", query, false)
		if err != nil || plan.Root == nil || plan.Format != "JSON" {
			t.Fatalf("MySQL default %q: %+v %v", query, plan, err)
		}
	}
	var count int
	var label string
	if err := target.QueryRow("SELECT count(*),min(label) FROM "+table).Scan(&count, &label); err != nil || count != 1 || label != "before" {
		t.Fatalf("MySQL default altered rows: %d %s %v", count, label, err)
	}
	if afterNext := autoIncrement(); afterNext != beforeNext {
		t.Fatalf("MySQL default advanced AUTO_INCREMENT: %d to %d", beforeNext, afterNext)
	}
	// Verify the next allocated ID too: information_schema statistics can be cached.
	inserted, err := target.Exec("INSERT INTO " + table + "(label) VALUES('after explain')")
	if err != nil {
		t.Fatal(err)
	}
	if nextID, err := inserted.LastInsertId(); err != nil || nextID != beforeNext {
		t.Fatalf("MySQL default consumed an AUTO_INCREMENT value: got %d, want %d (%v)", nextID, beforeNext, err)
	}
	tree, err := RunExplain(context.Background(), target, "mysql", "SELECT * FROM "+table+" WHERE label='before'", true)
	if err != nil || tree.Root == nil || tree.Format != "TREE" || tree.Root.ActualTime == nil {
		t.Fatalf("MySQL TREE metrics: %+v %v", tree, err)
	}
	if _, err := RunExplain(context.Background(), target, "mysql", "UPDATE "+table+" SET label='bad'", true); err != ErrExplainMySQLAnalyzeDML {
		t.Fatalf("MySQL ANALYZE DML accepted: %v", err)
	}
}

func TestExplainHTTPAuthorizationAndSSR(t *testing.T) {
	store, manager, saved, _, schema, _ := viewerFixture(t)
	pages := viewerPageRenderer{}
	for name, file := range map[string]string{"database-explain.html": "database/explain.html", "migration-editor.html": "migration/editor.html"} {
		page, err := template.ParseFiles("../../web/templates/layout.html", "../../web/templates/"+file)
		if err != nil {
			t.Fatal(err)
		}
		pages[name] = page
	}
	h := &ExplainHandler{Store: store, Manager: manager, Templates: pages}
	mux := http.NewServeMux()
	protect := access.RequireDatabaseAccess(store.db, access.AccessViewOnly)
	requireAuth := auth.RequireAuth(store.db, false)
	mux.Handle("GET /databases/{database_id}/explain", requireAuth(protect(http.HandlerFunc(h.Show))))
	mux.Handle("GET /databases/{database_id}/migration", requireAuth(access.RequireDatabaseAccess(store.db, access.AccessViewEdit)(http.HandlerFunc(h.ShowMigrationDraft))))
	mux.Handle("POST /databases/{database_id}/explain", requireAuth(LimitExplainBody(auth.CSRFProtectionMiddleware(protect(http.HandlerFunc(h.Analyze))))))
	uid, group := uuid.NewString(), uuid.NewString()
	if _, err := store.db.Exec("INSERT INTO users(id,username,password_hash,is_platform_admin) VALUES($1,$2,'test-only',true)", uid, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("INSERT INTO groups(id,name) VALUES($1,'Explain group')", group); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("INSERT INTO user_groups(user_id,group_id) VALUES($1,$2)", uid, group); err != nil {
		t.Fatal(err)
	}
	session, err := auth.CreateSession(context.Background(), store.db, uid)
	if err != nil {
		t.Fatal(err)
	}
	path := "/databases/" + saved.ID + "/explain"
	requestAt := func(requestPath, method string, form url.Values, token bool) (int, string) {
		t.Helper()
		var body string
		if form != nil {
			body = form.Encode()
		}
		r := httptest.NewRequest(method, requestPath, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: auth.CookieSessionName, Value: session.ID})
		if method == "POST" {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if token {
			r.Header.Set("X-CSRF-Token", session.CSRFToken)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	request := func(method string, form url.Values, token bool) (int, string) {
		return requestAt(path, method, form, token)
	}
	draftPath := "/databases/" + saved.ID + "/migration"
	if status, _ := requestAt(draftPath, "GET", nil, false); status != 403 {
		t.Fatalf("draft without grant: %d", status)
	}
	if status, _ := request("GET", nil, false); status != 403 {
		t.Fatalf("admin without target grant: %d", status)
	}
	if _, err := store.db.Exec("INSERT INTO group_database_grants(group_id,database_id,access_level) VALUES($1,$2,'VIEW_ONLY')", group, saved.ID); err != nil {
		t.Fatal(err)
	}
	if status, body := request("GET", nil, false); status != 200 || !strings.Contains(body, "Analisis rencana query") || strings.Contains(body, "sql_query=") {
		t.Fatalf("GET form: %d %s", status, body)
	}
	if status, _ := requestAt(draftPath, "GET", nil, false); status != 403 {
		t.Fatalf("VIEW_ONLY draft: %d", status)
	}
	q, _ := QuoteIdentifier("postgres", schema)
	query := "SELECT * FROM " + q + ".details WHERE id=1"
	form := url.Values{"sql_query": {query}}
	if status, _ := request("POST", form, false); status != 403 {
		t.Fatalf("missing CSRF: %d", status)
	}
	if status, body := request("POST", form, true); status != 200 || !strings.Contains(body, "Rencana eksekusi") || !strings.Contains(body, "Seq Scan") && !strings.Contains(body, "Index Scan") {
		t.Fatalf("VIEW_ONLY default: %d %s", status, body)
	}
	form.Set("analyze", "1")
	form.Set("confirm_analyze", "1")
	if status, _ := request("POST", form, true); status != 403 {
		t.Fatalf("VIEW_ONLY ANALYZE: %d", status)
	}
	if _, err := store.db.Exec("UPDATE group_database_grants SET access_level='VIEW_EDIT' WHERE group_id=$1", group); err != nil {
		t.Fatal(err)
	}
	if status, body := requestAt(draftPath, "GET", nil, false); status != 200 || !strings.Contains(body, "Analyze Query Plan") || !strings.Contains(body, `action="`+path+`"`) || !strings.Contains(body, session.CSRFToken) {
		t.Fatalf("VIEW_EDIT draft shortcut: %d %s", status, body)
	}
	if status, body := requestAt(path, "POST", url.Values{"sql_query": {query}, "csrf_token": {session.CSRFToken}}, false); status != 200 || !strings.Contains(body, "Mode standar") || !strings.Contains(body, "Rencana eksekusi") {
		t.Fatalf("migration shortcut default EXPLAIN: %d %s", status, body)
	}
	form.Del("confirm_analyze")
	if status, _ := request("POST", form, true); status != 422 {
		t.Fatalf("unconfirmed ANALYZE: %d", status)
	}
	form.Set("confirm_analyze", "1")
	if status, body := request("POST", form, true); status != 200 || !strings.Contains(body, "Mode ANALYZE") || !strings.Contains(body, "Waktu aktual") {
		t.Fatalf("confirmed ANALYZE: %d %s", status, body)
	}
	for _, bad := range []struct {
		query   string
		message string
	}{{"CREATE TABLE x(id INT)", ExplainDDLMessage}, {"SELECT 1; DROP TABLE x", ErrExplainStatement.Error()}} {
		form.Del("analyze")
		form.Del("confirm_analyze")
		form.Set("sql_query", bad.query)
		if status, body := request("POST", form, true); status != 422 || !strings.Contains(body, bad.message) || !strings.Contains(body, bad.query) {
			t.Fatalf("invalid SQL: %d %s", status, body)
		}
	}
	if _, err := store.db.Exec("DELETE FROM group_database_grants WHERE group_id=$1", group); err != nil {
		t.Fatal(err)
	}
	if status, _ := request("POST", url.Values{"sql_query": {query}}, true); status != 403 {
		t.Fatalf("revoked grant: %d", status)
	}
	if status, _ := requestAt(draftPath, "GET", nil, false); status != 403 {
		t.Fatalf("revoked draft: %d", status)
	}
}
