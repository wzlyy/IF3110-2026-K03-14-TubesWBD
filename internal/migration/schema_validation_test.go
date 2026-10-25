package migration

import (
	"testing"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
)

func TestTargetSchemaRejectsMigrationReceipts(t *testing.T) {
	for _, dbms := range []string{"postgres", "mysql"} {
		t.Run(dbms, func(t *testing.T) {
			desired, err := ParseTargetSchemaJSON([]byte(`{"tables":[{"name":"_dbplatform_migration_receipts","columns":[{"name":"id","type":"integer"}]}]}`), "public")
			if err == nil {
				_, err = CompareCatalogs(&database.Catalog{DBMS: dbms}, desired)
			}
			if err == nil {
				t.Fatal("reserved receipt table accepted as desired application table")
			}
		})
	}
}

func TestTargetSchemaRejectsInvalidObjects(t *testing.T) {
	cases := map[string]string{
		"unsupported dbms":       `{"dbms":"sqlite","tables":[{"name":"users","columns":[{"name":"id","type":"integer"}]}]}`,
		"duplicate table":        `{"tables":[{"name":"users","columns":[{"name":"id","type":"integer"}]},{"name":"users","columns":[{"name":"id","type":"integer"}]}]}`,
		"duplicate column":       `{"tables":[{"name":"users","columns":[{"name":"id","type":"integer"},{"name":"id","type":"integer"}]}]}`,
		"missing primary column": `{"tables":[{"name":"users","columns":[{"name":"id","type":"integer"}],"primary_key":["missing"]}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			desired, err := ParseTargetSchemaJSON([]byte(raw), "public")
			if err == nil {
				_, err = CompareCatalogs(&database.Catalog{DBMS: "postgres"}, desired)
			}
			if err == nil {
				t.Fatal("invalid schema accepted")
			}
		})
	}
}

func TestSchemaDefinitionCanonicalizationPreservesMeaning(t *testing.T) {
	for _, tc := range []struct {
		name, kind, old, next string
		equal                 bool
	}{
		{"check spacing", "CHECK", "CHECK ((id > 0))", "CHECK(id>0)", true},
		{"literal spacing", "CHECK", "CHECK (label = 'a  b')", "CHECK (label = 'a b')", false},
		{"quoted case", "CHECK", `CHECK ("Id" > 0)`, "CHECK (id > 0)", false},
		{"different reference schema", "FOREIGN KEY", "FOREIGN KEY (id) REFERENCES private.parent(id)", "FOREIGN KEY (id) REFERENCES public.parent(id)", false},
		{"qualified reference", "FOREIGN KEY", `FOREIGN KEY (id) REFERENCES "public".parent(id)`, "FOREIGN KEY (id) REFERENCES public.parent(id)", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := canonicalConstraint("postgres", tc.kind, tc.old) == canonicalConstraint("postgres", tc.kind, tc.next)
			if got != tc.equal {
				t.Fatalf("equivalent=%v, want %v", got, tc.equal)
			}
		})
	}
	if !equalDataTypes("character varying(100)", "varchar(100)") {
		t.Error("PostgreSQL varchar alias should be identical")
	}
}

func TestTargetSchemaRejectsInvalidReferencesAndConflicts(t *testing.T) {
	for name, raw := range map[string]string{
		"fk local definition column":   `{"tables":[{"name":"child","columns":[{"name":"id","type":"int"}],"constraints":[{"name":"fk","type":"FOREIGN KEY","definition":"FOREIGN KEY (missing) REFERENCES child(id)"}]}]}`,
		"fk target column":             `{"tables":[{"name":"child","columns":[{"name":"id","type":"int"}],"constraints":[{"name":"fk","type":"FOREIGN KEY","columns":["id"],"definition":"FOREIGN KEY (id) REFERENCES child(missing)"}]}]}`,
		"fk target table":              `{"tables":[{"name":"child","columns":[{"name":"id","type":"int"}],"constraints":[{"name":"fk","type":"FOREIGN KEY","columns":["id"],"definition":"FOREIGN KEY (id) REFERENCES missing(id)"}]}]}`,
		"constraint index collision":   `{"tables":[{"name":"child","columns":[{"name":"id","type":"int"}],"constraints":[{"name":"same","type":"UNIQUE","columns":["id"]}],"indexes":[{"name":"same","columns":["id"]}]}]}`,
		"schema index collision":       `{"tables":[{"name":"a","columns":[{"name":"id","type":"int"}],"indexes":[{"name":"same","columns":["id"]}]},{"name":"b","columns":[{"name":"id","type":"int"}],"indexes":[{"name":"same","columns":["id"]}]}]}`,
		"unknown unsupported property": `{"tables":[{"name":"a","columns":[{"name":"id","type":"int"}],"indexes":[{"name":"ix","columns":["id"],"where":"id > 0"}]}]}`,
		"conflicting container schema": `{"schemas":[{"name":"public","tables":[{"schema":"private","name":"a","columns":[{"name":"id","type":"int"}]}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			desired, err := ParseTargetSchemaJSON([]byte(raw), "public")
			if err == nil {
				_, err = CompareCatalogs(&database.Catalog{DBMS: "postgres"}, desired)
			}
			if err == nil {
				t.Fatal("invalid schema accepted")
			}
		})
	}
}
