package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/access"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
)

func TestConnectionsTemplateRegistry(t *testing.T) {
	web, err := findWebDir()
	if err != nil {
		t.Fatal(err)
	}
	templates, err := loadTemplates(web)
	if err != nil {
		t.Fatal(err)
	}
	for _, form := range []database.Connection{{DBMS: "postgres", Port: 5432}, {ID: "test-id", Name: "Example", DBMS: "postgres", Port: 5432}} {
		var out bytes.Buffer
		data := map[string]any{"CurrentUser": &auth.User{Username: "Admin", IsPlatformAdmin: true}, "CSRFToken": "test-token", "ActiveTab": "connections", "Form": form, "Connections": []database.Connection{form}}
		if err := templates.ExecuteTemplate(&out, "connections.html", data); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "<!DOCTYPE html>") || !strings.Contains(out.String(), "csrf_token") || !strings.Contains(out.String(), "Koneksi Database") {
			t.Fatal("connection page not rendered with layout")
		}
	}
}

func TestMigrationQueryPlanShortcut(t *testing.T) {
	web, err := findWebDir()
	if err != nil {
		t.Fatal(err)
	}
	templates, err := loadTemplates(web)
	if err != nil {
		t.Fatal(err)
	}
	for _, dbms := range []string{"postgres", "mysql"} {
		var out bytes.Buffer
		data := map[string]any{
			"CurrentUser": &auth.User{Username: "Editor"}, "CSRFToken": "test-token",
			"Connection": &database.Connection{ID: "target-id", Name: "Target", DBMS: dbms},
			"Access":     access.AccessViewEdit, "CanEdit": true, "ExplainURL": "/databases/target-id/explain",
			"SQLQuery": "SELECT '<script>alert(1)</script>'",
		}
		if err := templates.ExecuteTemplate(&out, "migration-editor.html", data); err != nil {
			t.Fatal(err)
		}
		body := out.String()
		for _, want := range []string{`method="POST"`, `action="/databases/target-id/explain"`, `name="sql_query"`, `name="csrf_token"`, "Analyze Query Plan", "&lt;script&gt;"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s migration shortcut missing %q", dbms, want)
			}
		}
		if strings.Contains(body, "<script>alert(1)</script>") || strings.Contains(body, `name="analyze"`) {
			t.Fatal("draft must escape SQL and submit safe default EXPLAIN")
		}
		if dbms == "mysql" && !strings.Contains(body, "implicit commit") {
			t.Fatal("missing MySQL DDL warning")
		}
	}
}
