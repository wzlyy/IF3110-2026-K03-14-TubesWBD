package migration

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
)

func compareAndGenerateSQL(t *testing.T, dbms string, tables []database.Table, raw string) (*DiffResult, string) {
	t.Helper()
	desired, err := ParseTargetSchemaJSON([]byte(raw), "public")
	if err != nil {
		t.Fatal(err)
	}
	diff, err := CompareCatalogs(&database.Catalog{DBMS: dbms, Name: "public", Schemas: []database.Schema{{Name: "public", Tables: tables}}}, desired)
	if err != nil {
		t.Fatal(err)
	}
	sql, err := GenerateMigrationSQL(diff, dbms)
	if err != nil {
		t.Fatal(err)
	}
	return diff, sql
}

func TestSchemaDiffExcludesMigrationReceipts(t *testing.T) {
	_, sql := compareAndGenerateSQL(t, "postgres", []database.Table{{Schema: "public", Name: "_dbplatform_migration_receipts"}}, `{"tables":[{"name":"users","columns":[{"name":"id","type":"integer"}]}]}`)
	if strings.Contains(strings.ToLower(sql), "_dbplatform_migration_receipts") {
		t.Fatalf("candidate modifies reserved receipt: %s", sql)
	}
	if _, err := ValidateSQL("postgres", sql); err != nil {
		t.Fatalf("generated SQL rejected by editor: %v; SQL=%s", err, sql)
	}
}

func TestSchemaDiffDetectsChangedConstraint(t *testing.T) {
	diff, _ := compareAndGenerateSQL(t, "postgres", []database.Table{{Schema: "public", Name: "users", Columns: []database.Column{{Name: "id", DataType: "integer"}}, Constraints: []database.Constraint{{Name: "positive_id", Type: "CHECK", Definition: "CHECK (id > 0)"}}}}, `{"tables":[{"name":"users","columns":[{"name":"id","type":"integer"}],"constraints":[{"name":"positive_id","type":"CHECK","definition":"CHECK (id > 10)"}]}]}`)
	if diff.IsIdentical {
		t.Fatal("CHECK (id > 0) -> CHECK (id > 10) reported identical")
	}
}

func TestSchemaDiffDetectsChangedIndex(t *testing.T) {
	diff, _ := compareAndGenerateSQL(t, "postgres", []database.Table{{Schema: "public", Name: "users", Columns: []database.Column{{Name: "id", DataType: "integer"}}, Indexes: []database.Index{{Name: "idx_users_id", Columns: []string{"id"}, Unique: false}}}}, `{"tables":[{"name":"users","columns":[{"name":"id","type":"integer"}],"indexes":[{"name":"idx_users_id","columns":["id"],"unique":true}]}]}`)
	if diff.IsIdentical {
		t.Fatal("nonunique -> unique index reported identical")
	}
}

func TestSchemaDiffPreservesTableCase(t *testing.T) {
	diff, _ := compareAndGenerateSQL(t, "postgres", []database.Table{{Schema: "public", Name: "Users", Columns: []database.Column{{Name: "id", DataType: "integer"}}}}, `{"tables":[{"name":"users","columns":[{"name":"id","type":"integer"}]}]}`)
	if diff.IsIdentical {
		t.Fatal(`distinct PostgreSQL tables "Users" and "users" reported identical`)
	}
}

func TestSchemaDiffMatchesMySQLPrimaryKey(t *testing.T) {
	diff, sql := compareAndGenerateSQL(t, "mysql", []database.Table{{Schema: "public", Name: "users", Columns: []database.Column{{Name: "id", DataType: "integer"}}, Constraints: []database.Constraint{{Name: "PRIMARY", Type: "PRIMARY KEY", Columns: []string{"id"}}}, Indexes: []database.Index{{Name: "PRIMARY", Columns: []string{"id"}, Primary: true, Unique: true}}}}, `{"dbms":"mysql","tables":[{"name":"users","columns":[{"name":"id","type":"integer"}],"primary_key":["id"]}]}`)
	if !diff.IsIdentical {
		t.Fatalf("identical MySQL primary key produces changes: %s", sql)
	}
}

func TestSchemaDiffDetectsChangedObjectDefinitions(t *testing.T) {
	cases := []struct {
		name    string
		current database.Constraint
		desired string
	}{
		{"primary key columns", database.Constraint{Name: "key_name", Type: "PRIMARY KEY", Columns: []string{"id"}}, `{"name":"key_name","type":"PRIMARY KEY","columns":["other"]}`},
		{"unique columns", database.Constraint{Name: "key_name", Type: "UNIQUE", Columns: []string{"id"}}, `{"name":"key_name","type":"UNIQUE","columns":["other"]}`},
		{"foreign key target", database.Constraint{Name: "key_name", Type: "FOREIGN KEY", Columns: []string{"id"}, Definition: `FOREIGN KEY (id) REFERENCES old_parent(id)`}, `{"name":"key_name","type":"FOREIGN KEY","columns":["id"],"definition":"FOREIGN KEY (id) REFERENCES new_parent(id)"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"tables":[{"name":"users","columns":[{"name":"id","type":"integer"},{"name":"other","type":"integer"}],"constraints":[` + tc.desired + `]}`
			if tc.current.Type == "FOREIGN KEY" {
				raw += `,{"name":"new_parent","columns":[{"name":"id","type":"integer"}],"primary_key":["id"]}`
			}
			diff, sql := compareAndGenerateSQL(t, "postgres", []database.Table{{Schema: "public", Name: "users", Columns: []database.Column{{Name: "id", DataType: "integer"}, {Name: "other", DataType: "integer"}}, Constraints: []database.Constraint{tc.current}}}, raw+`]}`)
			if diff.IsIdentical {
				t.Fatalf("same-name structural change reported identical: %s", tc.name)
			}
			drop, add := strings.Index(sql, "DROP CONSTRAINT"), strings.Index(sql, "ADD CONSTRAINT")
			if drop < 0 || add < 0 || drop > add {
				t.Fatalf("changed constraint must be replaced in dependency order: %s", sql)
			}
		})
	}
	t.Run("index columns", func(t *testing.T) {
		diff, sql := compareAndGenerateSQL(t, "postgres", []database.Table{{Schema: "public", Name: "users", Columns: []database.Column{{Name: "id", DataType: "integer"}, {Name: "other", DataType: "integer"}}, Indexes: []database.Index{{Name: "idx_users", Columns: []string{"id"}}}}}, `{"tables":[{"name":"users","columns":[{"name":"id","type":"integer"},{"name":"other","type":"integer"}],"indexes":[{"name":"idx_users","columns":["other"]}]}]}`)
		if diff.IsIdentical {
			t.Fatal("same-name index column change reported identical")
		}
		drop, add := strings.Index(sql, "DROP INDEX"), strings.Index(sql, "CREATE INDEX")
		if drop < 0 || add < 0 || drop > add {
			t.Fatalf("changed index must be replaced before recreating its name: %s", sql)
		}
	})
}

func TestSchemaDiffPreservesColumnAndObjectCase(t *testing.T) {
	cases := []struct {
		name, raw string
		table     database.Table
	}{
		{"column", `{"tables":[{"name":"users","columns":[{"name":"id","type":"integer"}]}]}`, database.Table{Schema: "public", Name: "users", Columns: []database.Column{{Name: "Id", DataType: "integer"}}}},
		{"constraint", `{"tables":[{"name":"users","columns":[{"name":"id","type":"integer"}],"constraints":[{"name":"positive","type":"CHECK","definition":"CHECK (id > 0)"}]}]}`, database.Table{Schema: "public", Name: "users", Columns: []database.Column{{Name: "id", DataType: "integer"}}, Constraints: []database.Constraint{{Name: "Positive", Type: "CHECK", Definition: "CHECK (id > 0)"}}}},
		{"index", `{"tables":[{"name":"users","columns":[{"name":"id","type":"integer"}],"indexes":[{"name":"idx","columns":["id"]}]}]}`, database.Table{Schema: "public", Name: "users", Columns: []database.Column{{Name: "id", DataType: "integer"}}, Indexes: []database.Index{{Name: "Idx", Columns: []string{"id"}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diff, _ := compareAndGenerateSQL(t, "postgres", []database.Table{tc.table}, tc.raw)
			if diff.IsIdentical {
				t.Fatal("distinct quoted identifiers reported identical")
			}
		})
	}
}

func TestSchemaDiffRejectsMismatchedDBMS(t *testing.T) {
	desired, err := ParseTargetSchemaJSON([]byte(`{"dbms":"mysql","tables":[{"name":"users","columns":[{"name":"id","type":"integer"}]}]}`), "public")
	if err != nil {
		return
	}
	_, err = CompareCatalogs(&database.Catalog{DBMS: "postgres"}, desired)
	if err == nil {
		t.Fatal("MySQL schema accepted for PostgreSQL target without validation")
	}
}

func TestSchemaDiffUsesMySQLTableCaseSetting(t *testing.T) {
	for _, mode := range []int{0, 1, 2} {
		t.Run(string(rune('0'+mode)), func(t *testing.T) {
			var current database.Catalog
			if err := json.Unmarshal([]byte(`{"DBMS":"mysql","Name":"public","LowerCaseTableNames":`+string(rune('0'+mode))+`,"Schemas":[{"Name":"public","Tables":[{"Schema":"public","Name":"Users","Columns":[{"Name":"id","DataType":"integer"}]}]}]}`), &current); err != nil {
				t.Fatal(err)
			}
			desired, err := ParseTargetSchemaJSON([]byte(`{"tables":[{"name":"users","columns":[{"name":"id","type":"integer"}]}]}`), "public")
			if err != nil {
				t.Fatal(err)
			}
			diff, err := CompareCatalogs(&current, desired)
			if err != nil {
				t.Fatal(err)
			}
			if diff.IsIdentical != (mode != 0) {
				t.Fatalf("lower_case_table_names=%d: identical=%v, changes=%+v", mode, diff.IsIdentical, diff.Changes)
			}
		})
	}
}

func TestSchemaDiffPreservesOmittedDefault(t *testing.T) {
	for _, dbms := range []string{"postgres", "mysql"} {
		t.Run(dbms, func(t *testing.T) {
			old := "7"
			diff, sql := compareAndGenerateSQL(t, dbms, []database.Table{{Schema: "public", Name: "users", Columns: []database.Column{{Name: "id", DataType: "integer", Default: &old}}}}, `{"tables":[{"name":"users","columns":[{"name":"id","type":"integer"}]}]}`)
			if !diff.IsIdentical {
				t.Fatalf("omitted default must preserve current value: %s", sql)
			}
			_, sql = compareAndGenerateSQL(t, dbms, []database.Table{{Schema: "public", Name: "users", Columns: []database.Column{{Name: "id", DataType: "integer", Default: &old}}}}, `{"tables":[{"name":"users","columns":[{"name":"id","type":"integer","nullable":true}]}]}`)
			if !strings.Contains(sql, "DEFAULT 7") || strings.Contains(sql, "DROP DEFAULT") {
				t.Fatalf("other column edits must retain omitted default: %s", sql)
			}
		})
	}
}

func TestSchemaDiffIgnoresPrimaryKeyNames(t *testing.T) {
	for _, dbms := range []string{"postgres", "mysql"} {
		t.Run(dbms, func(t *testing.T) {
			diff, sql := compareAndGenerateSQL(t, dbms, []database.Table{{Schema: "public", Name: "users", Columns: []database.Column{{Name: "id", DataType: "integer"}}, Constraints: []database.Constraint{{Name: "PRIMARY", Type: "PRIMARY KEY", Columns: []string{"id"}}}}}, `{"tables":[{"name":"users","columns":[{"name":"id","type":"integer"}],"constraints":[{"name":"users_pkey","type":"PRIMARY KEY","columns":["id"]}]}]}`)
			if !diff.IsIdentical {
				t.Fatalf("PK name alone must not replace a semantic key: %s", sql)
			}
		})
	}
}

func TestSchemaDiffPreservesUnrelatedForeignKeys(t *testing.T) {
	current := []database.Table{
		{Schema: "public", Name: "parent", Columns: []database.Column{{Name: "id", DataType: "integer"}}},
		{Schema: "public", Name: "child", Columns: []database.Column{{Name: "id", DataType: "integer"}}, Constraints: []database.Constraint{{Name: "child_fk", Type: "FOREIGN KEY", Columns: []string{"id"}, Definition: "FOREIGN KEY (id) REFERENCES public.parent(id)"}}},
		{Schema: "public", Name: "unrelated", Columns: []database.Column{{Name: "id", DataType: "integer"}}},
	}
	diff, sql := compareAndGenerateSQL(t, "postgres", current, `{"tables":[{"name":"parent","columns":[{"name":"id","type":"integer"}]},{"name":"child","columns":[{"name":"id","type":"integer"}],"constraints":[{"name":"child_fk","type":"FOREIGN KEY","columns":["id"],"definition":"FOREIGN KEY (id) REFERENCES public.parent(id)"}]},{"name":"unrelated","columns":[{"name":"id","type":"integer","nullable":true}]}]}`)
	for _, change := range diff.Changes {
		if change.Table != "unrelated" {
			t.Fatalf("unrelated FK must remain untouched: %s", sql)
		}
	}
}

func TestSchemaDiffPreservesMySQLReferenceTableCase(t *testing.T) {
	current := []database.Table{
		{Schema: "public", Name: "Parent", Columns: []database.Column{{Name: "id", DataType: "integer"}}},
		{Schema: "public", Name: "parent", Columns: []database.Column{{Name: "id", DataType: "integer"}}},
		{Schema: "public", Name: "child", Columns: []database.Column{{Name: "id", DataType: "integer"}}, Constraints: []database.Constraint{{Name: "fk", Type: "FOREIGN KEY", Columns: []string{"id"}, Definition: "FOREIGN KEY (id) REFERENCES public.Parent(id)"}}},
	}
	diff, sql := compareAndGenerateSQL(t, "mysql", current, `{"tables":[{"name":"Parent","columns":[{"name":"id","type":"integer"}]},{"name":"parent","columns":[{"name":"id","type":"integer"}]},{"name":"child","columns":[{"name":"id","type":"integer"}],"constraints":[{"name":"fk","type":"FOREIGN KEY","columns":["id"],"definition":"FOREIGN KEY (id) REFERENCES public.parent(id)"}]}]}`)
	if diff.IsIdentical {
		t.Fatalf("FK reference changed between case-sensitive table names: %s", sql)
	}
}
