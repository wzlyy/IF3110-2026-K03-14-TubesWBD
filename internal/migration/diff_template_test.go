package migration

import (
	"bytes"
	"html/template"
	"regexp"
	"testing"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
)

func TestDiffFormAllowsAlternativeFileInput(t *testing.T) {
	page, err := template.ParseFiles("../../web/templates/layout.html", "../../web/templates/migration/diff.html")
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	err = page.ExecuteTemplate(&body, "layout.html", map[string]any{
		"Connection": &database.Connection{ID: "test", Name: "Test", DBMS: "postgres"},
		"DiffURL":    "/databases/test/diff", "CanEdit": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	textarea := regexp.MustCompile(`<textarea\b[^>]*\bname="target_schema_json"[^>]*>`).FindString(body.String())
	file := regexp.MustCompile(`<input\b[^>]*\bname="schema_file"[^>]*>`).FindString(body.String())
	if textarea == "" || file == "" {
		t.Fatal("schema form must offer JSON text and file alternatives")
	}
	if regexp.MustCompile(`\srequired(?:\s|=|>)`).MatchString(textarea) {
		t.Fatal("required textarea blocks a valid file-only submission in browser validation")
	}
	if regexp.MustCompile(`\srequired(?:\s|=|>)`).MatchString(file) {
		t.Fatal("required file input blocks a valid text-only submission in browser validation")
	}
}
