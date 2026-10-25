//go:build integration

package migration

import (
	"context"
	"encoding/json"
	"html"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
)

func schemaChangesCatalog(t *testing.T, f *httpFixture, dbms string) *database.Catalog {
	t.Helper()
	catalog, err := database.LoadCatalog(context.Background(), f.target, dbms, f.connection.DBName)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func schemaChangesDefaultSchema(f *httpFixture, dbms string) string {
	if dbms == "mysql" {
		return f.connection.DBName
	}
	return "public"
}

func schemaChangesDiff(t *testing.T, f *httpFixture, dbms string, desired *TargetSchemaDefinition) *DiffResult {
	t.Helper()
	raw, err := json.Marshal(desired)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseTargetSchemaJSON(raw, schemaChangesDefaultSchema(f, dbms))
	if err != nil {
		t.Fatal(err)
	}
	diff, err := CompareCatalogs(schemaChangesCatalog(t, f, dbms), parsed)
	if err != nil {
		t.Fatal(err)
	}
	return diff
}

// Use the actual HTTP generator candidate, editor preview, runner, and history.
// No handcrafted substitute SQL is accepted at the execution boundary.
func schemaChangesApply(t *testing.T, f *httpFixture, desired *TargetSchemaDefinition) {
	t.Helper()
	raw, err := json.Marshal(desired)
	if err != nil {
		t.Fatal(err)
	}
	w := f.request("POST", "/databases/"+f.connection.ID+"/diff", url.Values{"target_schema_json": {string(raw)}}, true)
	if w.Code != 200 {
		t.Fatalf("schema diff: %d %s", w.Code, w.Body.String())
	}
	match := regexp.MustCompile(`<input\s+type="hidden"\s+name="sql_query"\s+value="([^"]*)"`).FindStringSubmatch(w.Body.String())
	if len(match) != 2 {
		t.Fatalf("generated candidate missing: %s", w.Body.String())
	}
	query := html.UnescapeString(match[1])
	t.Logf("generated candidate:\n%s", query)
	w = f.request("POST", f.base+"/preview", url.Values{"sql_query": {query}}, true)
	if w.Code != 200 {
		t.Fatalf("preview generated SQL: %d %s", w.Code, w.Body.String())
	}
	w = f.request("POST", f.base+"/execute", url.Values{"sql_query": {query}}, true)
	if w.Code != 303 {
		t.Fatalf("execute generated SQL: %d %s", w.Code, w.Body.String())
	}
	history, err := ListHistory(context.Background(), f.db, f.connection.ID)
	committed := false
	for _, attempt := range history {
		committed = committed || attempt.SQL == query && attempt.Status == Success
	}
	if err != nil || !committed {
		t.Fatalf("generated SQL must commit successfully: %+v %v", history, err)
	}
}

func schemaChangesTable(t *testing.T, desired *TargetSchemaDefinition, name string) *TableDef {
	t.Helper()
	for i := range desired.Tables {
		if desired.Tables[i].Name == name {
			return &desired.Tables[i]
		}
	}
	t.Fatalf("table %s missing from desired snapshot", name)
	return nil
}

func schemaChangesRelatedTables(t *testing.T, f *httpFixture) (string, string) {
	t.Helper()
	parent, child := f.table+"_parent", f.table+"_child"
	t.Cleanup(func() {
		_, _ = f.target.Exec("DROP TABLE IF EXISTS " + child)
		_, _ = f.target.Exec("DROP TABLE IF EXISTS " + parent)
	})
	return parent, child
}

func TestSchemaChangesCreateObjectsAndStableDiff(t *testing.T) {
	for _, dbms := range []string{"postgres", "mysql"} {
		t.Run(dbms, func(t *testing.T) {
			f := migrationHTTPFixture(t, dbms, "VIEW_EDIT")
			parent, child := schemaChangesRelatedTables(t, f)
			schema := schemaChangesDefaultSchema(f, dbms)
			parentQ, _ := database.QuoteIdentifier(dbms, parent)
			schemaQ, _ := database.QuoteIdentifier(dbms, schema)
			desired := desiredApplicationCatalog(schemaChangesCatalog(t, f, dbms))
			seven := "7"
			desired.Tables = append(desired.Tables,
				TableDef{Schema: schema, Name: parent,
					Columns:    []ColumnDef{{Name: "id", DataType: "integer"}, {Name: "alt", DataType: "integer"}, {Name: "score", DataType: "integer", Default: &seven}},
					PrimaryKey: []string{"id"},
					Constraints: []ConstraintDef{
						{Name: parent + "_uq", Type: "UNIQUE", Columns: []string{"alt"}},
						{Name: parent + "_check", Type: "CHECK", Columns: []string{"score"}, Definition: "CHECK (score > 0)"},
					},
					Indexes: []IndexDef{{Name: parent + "_score", Columns: []string{"score"}}},
				},
				TableDef{Schema: schema, Name: child,
					Columns:     []ColumnDef{{Name: "id", DataType: "integer"}, {Name: "parent_id", DataType: "integer"}},
					PrimaryKey:  []string{"id"},
					Constraints: []ConstraintDef{{Name: child + "_fk", Type: "FOREIGN KEY", Columns: []string{"parent_id"}, Definition: "FOREIGN KEY (parent_id) REFERENCES " + schemaQ + "." + parentQ + "(id)"}},
				},
			)
			schemaChangesApply(t, f, desired)
			fresh := schemaChangesCatalog(t, f, dbms)
			created, exists := fresh.FindTable(schema, parent)
			if !exists {
				t.Fatal("generated parent table missing")
			}
			var primary, unique, check, index bool
			for _, c := range created.Constraints {
				primary = primary || c.Type == "PRIMARY KEY"
				unique = unique || c.Name == parent+"_uq" && c.Type == "UNIQUE"
				check = check || c.Name == parent+"_check" && c.Type == "CHECK"
			}
			for _, i := range created.Indexes {
				index = index || i.Name == parent+"_score"
			}
			if !primary || !unique || !check || !index {
				t.Fatalf("new table objects missing: %+v", created)
			}
			for _, query := range []string{"INSERT INTO " + parent + " VALUES(1, 1, 1)", "INSERT INTO " + child + " VALUES(1, 1)"} {
				if _, err := f.target.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			for _, query := range []string{"INSERT INTO " + parent + " VALUES(2, 1, 1)", "INSERT INTO " + parent + " VALUES(2, 2, 0)", "INSERT INTO " + child + " VALUES(2, 999)"} {
				if _, err := f.target.Exec(query); err == nil {
					t.Fatalf("generated constraint did not enforce data rule: %s", query)
				}
			}
			t.Run("fresh catalog snapshot identical", func(t *testing.T) {
				if diff := schemaChangesDiff(t, f, dbms, desiredApplicationCatalog(fresh)); !diff.IsIdentical {
					t.Fatalf("unchanged catalog snapshot must be identical: %+v", diff.Changes)
				}
			})
			t.Run("repeated original JSON identical", func(t *testing.T) {
				if diff := schemaChangesDiff(t, f, dbms, desired); !diff.IsIdentical {
					t.Fatalf("repeated same JSON must not recreate equivalent objects: %+v", diff.Changes)
				}
			})
		})
	}
}

func TestSchemaChangesReplaceCheckIndexAndDropDefault(t *testing.T) {
	for _, dbms := range []string{"postgres", "mysql"} {
		t.Run(dbms, func(t *testing.T) {
			f := migrationHTTPFixture(t, dbms, "VIEW_EDIT")
			checkName, indexName := f.table+"_check", f.table+"_idx"
			prior := f.execute(t, "CREATE TABLE "+f.table+"(id integer PRIMARY KEY, score integer NOT NULL DEFAULT 7, other integer NOT NULL, CONSTRAINT "+checkName+" CHECK (score > 0)); CREATE INDEX "+indexName+" ON "+f.table+"(score)")
			if prior.Status != Success {
				t.Fatalf("fixture creation failed: %+v", prior)
			}
			desired := desiredApplicationCatalog(schemaChangesCatalog(t, f, dbms))
			table := schemaChangesTable(t, desired, f.table)
			for i := range table.Columns {
				if table.Columns[i].Name == "score" {
					table.Columns[i].Default = nil
				}
			}
			for i := range table.Constraints {
				if table.Constraints[i].Name == checkName {
					table.Constraints[i].Definition = "CHECK (score > 10)"
				}
			}
			for i := range table.Indexes {
				if table.Indexes[i].Name == indexName {
					table.Indexes[i].Columns = []string{"other"}
					table.Indexes[i].Unique = true
				}
			}
			schemaChangesApply(t, f, desired)
			fresh := schemaChangesCatalog(t, f, dbms)
			actual, exists := fresh.FindTable(schemaChangesDefaultSchema(f, dbms), f.table)
			if !exists {
				t.Fatal("modified table missing")
			}
			for _, c := range actual.Columns {
				if c.Name == "score" && c.Default != nil {
					t.Fatalf("old default retained: %s", *c.Default)
				}
			}
			var replacedIndex bool
			for _, idx := range actual.Indexes {
				replacedIndex = replacedIndex || idx.Name == indexName && idx.Unique && len(idx.Columns) == 1 && idx.Columns[0] == "other"
			}
			if !replacedIndex {
				t.Fatalf("same-name index replacement not applied: %+v", actual.Indexes)
			}
			if _, err := f.target.Exec("INSERT INTO " + f.table + " VALUES(1, 11, 10)"); err != nil {
				t.Fatal(err)
			}
			for _, query := range []string{"INSERT INTO " + f.table + " VALUES(2, 1, 20)", "INSERT INTO " + f.table + " VALUES(2, 12, 10)"} {
				if _, err := f.target.Exec(query); err == nil {
					t.Fatalf("replacement constraint/index did not enforce new rule: %s", query)
				}
			}
			if diff := schemaChangesDiff(t, f, dbms, desired); !diff.IsIdentical {
				t.Fatalf("repeated modified schema must be identical: %+v", diff.Changes)
			}
		})
	}
}

func TestSchemaChangesReplacePrimaryKeyWithSurvivingForeignKey(t *testing.T) {
	for _, dbms := range []string{"postgres", "mysql"} {
		t.Run(dbms, func(t *testing.T) {
			f := migrationHTTPFixture(t, dbms, "VIEW_EDIT")
			parent, child := schemaChangesRelatedTables(t, f)
			prior := f.execute(t, "CREATE TABLE "+parent+"(id integer PRIMARY KEY, alt integer NOT NULL); CREATE TABLE "+child+"(id integer PRIMARY KEY, parent_id integer NOT NULL, CONSTRAINT "+child+"_fk FOREIGN KEY(parent_id) REFERENCES "+parent+"(id))")
			if prior.Status != Success {
				t.Fatalf("fixture creation failed: %+v", prior)
			}
			desired := desiredApplicationCatalog(schemaChangesCatalog(t, f, dbms))
			table := schemaChangesTable(t, desired, parent)
			for i := range table.Constraints {
				if table.Constraints[i].Type == "PRIMARY KEY" {
					table.Constraints[i].Columns = []string{"alt"}
					table.Constraints[i].Definition = ""
				}
			}
			table.Constraints = append(table.Constraints, ConstraintDef{Name: parent + "_id_unique", Type: "UNIQUE", Columns: []string{"id"}})
			schemaChangesApply(t, f, desired)
			fresh := schemaChangesCatalog(t, f, dbms)
			actual, exists := fresh.FindTable(schemaChangesDefaultSchema(f, dbms), parent)
			if !exists {
				t.Fatal("parent table missing")
			}
			var primary bool
			for _, c := range actual.Constraints {
				primary = primary || c.Type == "PRIMARY KEY" && len(c.Columns) == 1 && c.Columns[0] == "alt"
			}
			if !primary {
				t.Fatalf("primary key replacement not applied: %+v", actual.Constraints)
			}
			if _, err := f.target.Exec("INSERT INTO " + parent + " VALUES(1, 10)"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.target.Exec("INSERT INTO " + child + " VALUES(1, 1)"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.target.Exec("INSERT INTO " + child + " VALUES(2, 999)"); err == nil {
				t.Fatal("surviving foreign key no longer enforced")
			}
			if diff := schemaChangesDiff(t, f, dbms, desired); !diff.IsIdentical {
				t.Fatalf("unchanged FK and replacement PK should settle to identical: %+v", diff.Changes)
			}
		})
	}
}

func TestSchemaChangesDropRelatedTables(t *testing.T) {
	for _, dbms := range []string{"postgres", "mysql"} {
		t.Run(dbms, func(t *testing.T) {
			f := migrationHTTPFixture(t, dbms, "VIEW_EDIT")
			// Sort the referenced table before its referencing table to expose naive DROP ordering.
			parent, child := f.table+"_a_parent", f.table+"_z_child"
			t.Cleanup(func() {
				_, _ = f.target.Exec("DROP TABLE IF EXISTS " + child)
				_, _ = f.target.Exec("DROP TABLE IF EXISTS " + parent)
			})
			prior := f.execute(t, "CREATE TABLE "+parent+"(id integer PRIMARY KEY); CREATE TABLE "+child+"(id integer PRIMARY KEY, CONSTRAINT "+child+"_fk FOREIGN KEY(id) REFERENCES "+parent+"(id)); CREATE TABLE "+f.table+"(id integer)")
			if prior.Status != Success {
				t.Fatalf("fixture creation failed: %+v", prior)
			}
			desired := desiredApplicationCatalog(schemaChangesCatalog(t, f, dbms))
			var retained []TableDef
			for _, table := range desired.Tables {
				if table.Name != parent && table.Name != child {
					retained = append(retained, table)
				}
			}
			desired.Tables = retained
			schemaChangesApply(t, f, desired)
			fresh := schemaChangesCatalog(t, f, dbms)
			for _, name := range []string{parent, child} {
				if _, exists := fresh.FindTable(schemaChangesDefaultSchema(f, dbms), name); exists {
					t.Fatalf("related table %s not removed", name)
				}
			}
			if _, exists := fresh.FindTable(schemaChangesDefaultSchema(f, dbms), f.table); !exists {
				t.Fatal("unrelated desired table was removed")
			}
			if diff := schemaChangesDiff(t, f, dbms, desired); !diff.IsIdentical {
				t.Fatalf("drop related tables did not settle to identical: %+v", diff.Changes)
			}
		})
	}
}

func TestCatalogForeignKeyReferenceIsSchemaQualified(t *testing.T) {
	f := migrationHTTPFixture(t, "postgres", "VIEW_EDIT")
	parent, child := schemaChangesRelatedTables(t, f)
	if result := f.execute(t, "CREATE TABLE "+parent+"(id int PRIMARY KEY); CREATE TABLE "+child+"(id int REFERENCES public."+parent+"(id))"); result.Status != Success {
		t.Fatal(result)
	}
	catalog := schemaChangesCatalog(t, f, "postgres")
	table, _ := catalog.FindTable("public", child)
	for _, con := range table.Constraints {
		if con.Type == "FOREIGN KEY" && !strings.Contains(con.Definition, "REFERENCES public.") {
			t.Fatalf("catalog reference must not depend on target search_path: %s", con.Definition)
		}
	}
}

func TestSchemaChangesForeignKeyUsesNewUniqueIndex(t *testing.T) {
	for _, dbms := range []string{"postgres", "mysql"} {
		t.Run(dbms, func(t *testing.T) {
			f := migrationHTTPFixture(t, dbms, "VIEW_EDIT")
			parent, child := schemaChangesRelatedTables(t, f)
			schema := schemaChangesDefaultSchema(f, dbms)
			pq, _ := database.QuoteIdentifier(dbms, parent)
			sq, _ := database.QuoteIdentifier(dbms, schema)
			desired := desiredApplicationCatalog(schemaChangesCatalog(t, f, dbms))
			desired.Tables = append(desired.Tables,
				TableDef{Schema: schema, Name: parent, Columns: []ColumnDef{{Name: "id", DataType: "integer"}}, Indexes: []IndexDef{{Name: parent + "_unique", Columns: []string{"id"}, Unique: true}}},
				TableDef{Schema: schema, Name: child, Columns: []ColumnDef{{Name: "id", DataType: "integer"}}, Constraints: []ConstraintDef{{Name: child + "_fk", Type: "FOREIGN KEY", Columns: []string{"id"}, Definition: "FOREIGN KEY (id) REFERENCES " + sq + "." + pq + "(id)"}}},
			)
			schemaChangesApply(t, f, desired)
			if _, err := f.target.Exec("INSERT INTO " + child + " VALUES(999)"); err == nil {
				t.Fatal("FK backed by generated unique index must reject orphan")
			}
			if diff := schemaChangesDiff(t, f, dbms, desired); !diff.IsIdentical {
				t.Fatalf("repeated schema must be identical: %+v", diff.Changes)
			}
		})
	}
}
