//go:build integration

package database

import (
	"context"
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/access"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
)

func mysqlTargetFixture(t *testing.T) (*ConnectionStore, *TargetManager, *Connection, *sql.DB, string) {
	t.Helper()
	if os.Getenv("TARGET_MYSQL_HOST") == "" {
		t.Skip("dedicated MySQL test environment not configured")
	}
	store, manager := connectionFixture(t)
	port, err := strconv.Atoi(os.Getenv("TARGET_MYSQL_PORT"))
	if err != nil {
		t.Fatal(err)
	}
	c := Connection{
		Name: "Integration MySQL", DBMS: "mysql", Host: os.Getenv("TARGET_MYSQL_HOST"),
		Port: port, DBName: os.Getenv("TARGET_MYSQL_NAME"), Username: os.Getenv("TARGET_MYSQL_USER"),
	}
	password := os.Getenv("TARGET_MYSQL_PASSWORD")
	saved, err := store.Create(context.Background(), c, password)
	if err != nil {
		t.Fatal(err)
	}
	var ciphertext string
	if err = store.db.QueryRow("SELECT password_encrypted FROM database_connections WHERE id=$1", saved.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if ciphertext == password || strings.Contains(ciphertext, password) {
		t.Fatal("MySQL password stored in plaintext")
	}
	if err = manager.Test(context.Background(), saved.ID); err != nil {
		t.Fatal(err)
	}
	target, dbms, err := manager.Open(context.Background(), saved.ID)
	if err != nil || dbms != "mysql" || target.Stats().MaxOpenConnections != 5 {
		t.Fatalf("first MySQL open: %s %v", dbms, err)
	}
	cached, cachedDBMS, err := manager.Open(context.Background(), saved.ID)
	if err != nil || cached != target || cachedDBMS != "mysql" {
		t.Fatalf("cached MySQL DBMS/pool lost: %s %v", cachedDBMS, err)
	}
	prefix := "mysql_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	return store, manager, saved, target, prefix
}

func TestMySQLCatalogDataAndConnection(t *testing.T) {
	store, manager, saved, target, prefix := mysqlTargetFixture(t)
	provisioning := mysqlProvisioningFixture(t)
	ctx := context.Background()
	database := saved.DBName
	qdb, _ := QuoteIdentifier("mysql", database)
	quoteTable := func(suffix string) string {
		t.Helper()
		q, err := QuoteIdentifier("mysql", prefix+suffix)
		if err != nil {
			t.Fatal(err)
		}
		return qdb + "." + q
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := target.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	otherDatabase := prefix + "_other"
	qother, _ := QuoteIdentifier("mysql", otherDatabase)
	if _, err := provisioning.Exec("CREATE DATABASE " + qother); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, suffix := range []string{"_details", "_scalars", "_quoted`&/雪", "_rows_0", "_rows_50", "_rows_51", "_rows_101", "_parent"} {
			if _, err := target.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+quoteTable(suffix)); err != nil {
				t.Error(err)
			}
		}
		if _, err := provisioning.ExecContext(context.Background(), "DROP DATABASE "+qother); err != nil {
			t.Error(err)
		}
	})
	if _, err := provisioning.Exec("CREATE TABLE " + qother + ".outside_table(id INT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	exec("CREATE TABLE " + quoteTable("_parent") + "(id INT PRIMARY KEY, second_id INT, UNIQUE KEY pair_unique(id,second_id))")
	exec("CREATE TABLE " + quoteTable("_details") +
		"(id INT PRIMARY KEY,label VARCHAR(100) DEFAULT 'hello',parent_id INT,CONSTRAINT " +
		"`" + prefix + "_collision` CHECK (id>0),CONSTRAINT `" + prefix + "_fk` FOREIGN KEY(parent_id) REFERENCES " +
		quoteTable("_parent") + "(id),CONSTRAINT `" + prefix + "_collision` UNIQUE(label))")
	exec("CREATE INDEX `" + prefix + "_expr` ON " + quoteTable("_details") + " ((lower(label)),id DESC)")
	exec("CREATE UNIQUE INDEX `" + prefix + "_unique_expr` ON " + quoteTable("_details") + " ((lower(label)),id)")
	exec("CREATE VIEW " + qdb + ".`" + prefix + "_view` AS SELECT 1 AS id")
	t.Cleanup(func() {
		if _, err := target.ExecContext(context.Background(), "DROP VIEW IF EXISTS "+qdb+".`"+prefix+"_view`"); err != nil {
			t.Error(err)
		}
	})
	exec("CREATE TABLE " + quoteTable("_scalars") +
		"(id INT AUTO_INCREMENT PRIMARY KEY,base INT,doubled INT GENERATED ALWAYS AS (base*2) STORED,payload BLOB)")
	exec("INSERT INTO "+quoteTable("_scalars")+"(base,payload) VALUES(?,?)", 2, []byte{0, 255})
	for _, count := range []int{0, 50, 51, 101} {
		suffix := fmt.Sprintf("_rows_%d", count)
		exec("CREATE TABLE " + quoteTable(suffix) + "(id INT PRIMARY KEY,value TEXT NULL)")
		for i := 1; i <= count; i++ {
			var value any = fmt.Sprintf("row-%d", i)
			switch i {
			case 1:
				value = nil
			case 2:
				value = ""
			case 3:
				value = "<script>alert(1)</script>"
			}
			exec("INSERT INTO "+quoteTable(suffix)+"(id,value) VALUES(?,?)", i, value)
		}
	}
	weird := prefix + "_quoted`&/雪"
	exec("CREATE TABLE " + quoteTable("_quoted`&/雪") + "(id INT PRIMARY KEY,value TEXT)")
	exec("INSERT INTO " + quoteTable("_quoted`&/雪") + " VALUES(1,'quoted table')")

	catalog, err := LoadCatalog(ctx, target, "mysql", database)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Schemas) != 1 || catalog.Schemas[0].Name != database || catalog.DBMS != "mysql" {
		t.Fatalf("cross-database catalog: %+v", catalog)
	}
	if _, ok := catalog.FindTable(otherDatabase, "outside_table"); ok {
		t.Fatal("other MySQL database exposed")
	}
	if _, ok := catalog.FindTable(database, prefix+"_view"); ok {
		t.Fatal("view exposed as table")
	}
	details, ok := catalog.FindTable(database, prefix+"_details")
	if !ok || len(details.Columns) != 3 || details.Columns[1].Default == nil || *details.Columns[1].Default != "hello" || details.Columns[0].Nullable {
		t.Fatalf("details metadata: %+v", details)
	}
	kinds := map[string]bool{}
	for _, con := range details.Constraints {
		kinds[con.Type] = true
		if con.Definition == "" {
			t.Fatalf("empty MySQL constraint: %+v", con)
		}
		if con.Type == "FOREIGN KEY" && (!strings.Contains(con.Definition, "REFERENCES") || len(con.Columns) != 1 || con.Columns[0] != "parent_id") {
			t.Fatalf("foreign key metadata: %+v", con)
		}
	}
	for _, kind := range []string{"PRIMARY KEY", "FOREIGN KEY", "UNIQUE", "CHECK"} {
		if !kinds[kind] {
			t.Fatalf("missing MySQL constraint %s", kind)
		}
	}
	collision := map[string]Constraint{}
	var functionalUnique Constraint
	for _, con := range details.Constraints {
		if con.Name == prefix+"_collision" {
			collision[con.Type] = con
		}
		if con.Name == prefix+"_unique_expr" {
			functionalUnique = con
		}
	}
	if len(collision) != 2 || len(collision["UNIQUE"].Columns) != 1 || collision["UNIQUE"].Columns[0] != "label" || len(collision["CHECK"].Columns) != 0 || !strings.Contains(collision["CHECK"].Definition, "id") {
		t.Fatalf("same-name constraints merged or crossed: %+v", collision)
	}
	if functionalUnique.Type != "UNIQUE" || len(functionalUnique.Columns) != 2 || !strings.Contains(strings.ToLower(functionalUnique.Columns[0]), "lower") || !strings.Contains(functionalUnique.Definition, "lower") {
		t.Fatalf("functional UNIQUE constraint lost index expression: %+v", functionalUnique)
	}
	foundExpression := false
	for _, index := range details.Indexes {
		if index.Name == prefix+"_expr" {
			foundExpression = len(index.Columns) == 2 && strings.Contains(strings.ToLower(index.Columns[0]), "lower") && strings.Contains(index.Definition, "DESC")
		}
	}
	if !foundExpression {
		t.Fatalf("MySQL expression index missing: %+v", details.Indexes)
	}
	scalars, _ := catalog.FindTable(database, prefix+"_scalars")
	if scalars.Columns[0].Identity != "auto_increment" || !strings.Contains(scalars.Columns[2].Generated, "STORED GENERATED") {
		t.Fatalf("MySQL generated columns: %+v", scalars.Columns)
	}
	scalarData, err := ReadTableData(ctx, target, "mysql", *scalars, 1)
	if err != nil || scalarData.Rows[0][3].Text != "\\x00ff" {
		t.Fatalf("MySQL scalar values: %+v %v", scalarData, err)
	}
	for _, count := range []int{0, 50, 51, 101} {
		table, _ := catalog.FindTable(database, fmt.Sprintf("%s_rows_%d", prefix, count))
		for page := int64(1); page <= 3; page++ {
			data, err := ReadTableData(ctx, target, "mysql", *table, page)
			if err != nil {
				t.Fatal(err)
			}
			remaining := count - int(page-1)*50
			want := max(0, min(50, remaining))
			if len(data.Rows) != want || data.HasNext != (remaining > 50) || !data.OrderedByPrimaryKey {
				t.Fatalf("MySQL count%d page%d: %d next%v", count, page, len(data.Rows), data.HasNext)
			}
			if page == 1 && count > 2 && (!data.Rows[0][1].Null || data.Rows[1][1].Null || data.Rows[1][1].Text != "" || data.Rows[2][1].Text != "<script>alert(1)</script>") {
				t.Fatal("MySQL NULL/empty/HTML values lost")
			}
		}
	}
	quoted, ok := catalog.FindTable(database, weird)
	if !ok {
		t.Fatal("quoted MySQL table missing")
	}
	data, err := ReadTableData(ctx, target, "mysql", *quoted, 1)
	if err != nil || data.Rows[0][1].Text != "quoted table" {
		t.Fatalf("quoted MySQL data: %+v %v", data, err)
	}
	if _, err := ReadTableData(ctx, target, "mysql", Table{Schema: otherDatabase, Name: "outside_table"}, 1); err != ErrTableNotFound {
		t.Fatalf("unvalidated table accessed: %v", err)
	}
	if _, err := LoadCatalog(ctx, target, "mysql", otherDatabase); err != ErrTargetUnavailable {
		t.Fatalf("catalog accepted mismatched database: %v", err)
	}

	// Edited passwords invalidate the typed cache without exposing driver errors.
	var encrypted string
	if err := store.db.QueryRow("SELECT password_encrypted FROM database_connections WHERE id=$1", saved.ID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, saved.ID, *saved, ""); err != nil {
		t.Fatal(err)
	}
	manager.Invalidate(saved.ID)
	var preserved string
	if err := store.db.QueryRow("SELECT password_encrypted FROM database_connections WHERE id=$1", saved.ID).Scan(&preserved); err != nil || preserved != encrypted {
		t.Fatalf("MySQL password was not preserved: %v", err)
	}
	if err := store.Update(ctx, saved.ID, *saved, "wrong-test-password"); err != nil {
		t.Fatal(err)
	}
	manager.Invalidate(saved.ID)
	if err := manager.Test(ctx, saved.ID); err == nil || strings.Contains(err.Error(), "wrong-test-password") {
		t.Fatalf("MySQL failure leaked secret: %v", err)
	}
	if err := store.Update(ctx, saved.ID, *saved, os.Getenv("TARGET_MYSQL_PASSWORD")); err != nil {
		t.Fatal(err)
	}
	manager.Invalidate(saved.ID)
	target, _, err = manager.Open(ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
}

func TestMySQLViewerAuthorizationAndSSR(t *testing.T) {
	store, manager, saved, target, prefix := mysqlTargetFixture(t)
	qdb, _ := QuoteIdentifier("mysql", saved.DBName)
	qtable, _ := QuoteIdentifier("mysql", prefix+"_rows")
	if _, err := target.Exec("CREATE TABLE " + qdb + "." + qtable + "(id INT PRIMARY KEY,value TEXT)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = target.Exec("DROP TABLE IF EXISTS " + qdb + "." + qtable) })
	if _, err := target.Exec("INSERT INTO " + qdb + "." + qtable + " VALUES(1,NULL),(2,''),(3,'<script>alert(1)</script>')"); err != nil {
		t.Fatal(err)
	}
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
	userID := uuid.NewString()
	if _, err := store.db.Exec("INSERT INTO users(id,username,password_hash,is_platform_admin) VALUES($1,$2,'test-only',false)", userID, userID); err != nil {
		t.Fatal(err)
	}
	session, err := auth.CreateSession(context.Background(), store.db, userID)
	if err != nil {
		t.Fatal(err)
	}
	request := func(path string) (int, string) {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(&http.Cookie{Name: auth.CookieSessionName, Value: session.ID})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	if status, body := request("/databases"); status != 200 || strings.Contains(body, saved.Name) {
		t.Fatalf("ungranted MySQL list: %d", status)
	}
	for _, view := range []string{"schema", "data"} {
		if status, _ := request("/databases/" + saved.ID + "/" + view); status != 403 {
			t.Fatalf("ungranted MySQL %s: %d", view, status)
		}
	}
	groupID := uuid.NewString()
	if _, err := store.db.Exec("INSERT INTO groups(id,name) VALUES($1,'MySQL viewer')", groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("INSERT INTO user_groups(user_id,group_id) VALUES($1,$2)", userID, groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("INSERT INTO group_database_grants(group_id,database_id,access_level) VALUES($1,$2,'VIEW_ONLY')", groupID, saved.ID); err != nil {
		t.Fatal(err)
	}
	if status, body := request("/databases"); status != 200 || !strings.Contains(body, saved.Name) || !strings.Contains(body, "MySQL") {
		t.Fatalf("granted MySQL list: %d", status)
	}
	if status, body := request(tableURL(saved.ID, "schema", saved.DBName, prefix+"_rows", 1)); status != 200 || !strings.Contains(body, "MySQL") || !strings.Contains(body, prefix+"_rows") {
		t.Fatalf("MySQL schema SSR: %d", status)
	}
	if status, body := request(tableURL(saved.ID, "data", saved.DBName, prefix+"_rows", 1)); status != 200 || !strings.Contains(body, "MySQL") || !strings.Contains(body, "(string kosong)") || !strings.Contains(body, "&lt;script&gt;") || strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatalf("MySQL data SSR: %d", status)
	}
	for _, query := range []url.Values{
		{"schema": {prefix + "_outside"}, "table": {"outside_table"}},
		{"schema": {saved.DBName}, "table": {prefix + "_rows`; DROP TABLE x;--"}},
	} {
		if status, _ := request("/databases/" + saved.ID + "/data?" + query.Encode()); status != 404 {
			t.Fatalf("MySQL unsafe schema/table accepted: %d", status)
		}
	}
	if _, err := store.db.Exec("DELETE FROM group_database_grants WHERE group_id=$1", groupID); err != nil {
		t.Fatal(err)
	}
	if status, _ := request("/databases/" + saved.ID + "/data"); status != 403 {
		t.Fatalf("MySQL revoked URL remained accessible: %d", status)
	}
}

func TestMySQLPunctuatedCredentials(t *testing.T) {
	store, manager, saved, _, prefix := mysqlTargetFixture(t)
	target := mysqlProvisioningFixture(t)
	username, password := prefix+"_user", "p@ss /?&:#"
	account := "'" + username + "'@'%'"
	if _, err := target.Exec("CREATE USER " + account + " IDENTIFIED BY 'p@ss /?&:#'"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := target.Exec("DROP USER " + account); err != nil {
			t.Error(err)
		}
	})
	qdb, _ := mysqlGrantScope(saved.DBName)
	if _, err := target.Exec("GRANT SELECT, SHOW VIEW, TRIGGER ON " + qdb + ".* TO " + account); err != nil {
		t.Fatal(err)
	}
	withPunctuation := *saved
	withPunctuation.Username = username
	created, err := store.Create(context.Background(), withPunctuation, password)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Test(context.Background(), created.ID); err != nil {
		t.Fatalf("punctuated MySQL password did not authenticate: %v", err)
	}
	manager.Invalidate(created.ID)
}

func TestTargetDBMSEditInvalidatesPool(t *testing.T) {
	store, manager, mysqlConnection, _, _ := mysqlTargetFixture(t)
	ctx := context.Background()
	postgresConnection, postgresPassword := targetFixture()
	if err := store.Update(ctx, mysqlConnection.ID, postgresConnection, postgresPassword); err != nil {
		t.Fatal(err)
	}
	manager.Invalidate(mysqlConnection.ID)
	pool, dbms, err := manager.Open(ctx, mysqlConnection.ID)
	if err != nil || dbms != "postgres" || pool.PingContext(ctx) != nil {
		t.Fatalf("edited PostgreSQL target: %s %v", dbms, err)
	}
	if err := store.Update(ctx, mysqlConnection.ID, *mysqlConnection, os.Getenv("TARGET_MYSQL_PASSWORD")); err != nil {
		t.Fatal(err)
	}
	manager.Invalidate(mysqlConnection.ID)
	pool, dbms, err = manager.Open(ctx, mysqlConnection.ID)
	if err != nil || dbms != "mysql" || pool.PingContext(ctx) != nil {
		t.Fatalf("edited MySQL target: %s %v", dbms, err)
	}
}
