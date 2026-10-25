package migration

import (
	"strings"
	"testing"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
)

func TestGenerateMigrationSQLIncludesTableObjects(t *testing.T) {
	_, sql := compareAndGenerateSQL(t, "postgres", nil, `{"tables":[{"name":"users","columns":[{"name":"id","type":"integer"}],"constraints":[{"name":"positive_id","type":"CHECK","definition":"CHECK (id > 0)"}],"indexes":[{"name":"idx_users_id","columns":["id"]}]}]}`)
	if !strings.Contains(sql, "positive_id") || !strings.Contains(sql, "idx_users_id") {
		t.Fatalf("new table silently loses constraints/indexes: %s", sql)
	}
}

func TestGenerateMigrationSQLDropsDefault(t *testing.T) {
	old := "7"
	_, sql := compareAndGenerateSQL(t, "postgres", []database.Table{{Schema: "public", Name: "users", Columns: []database.Column{{Name: "id", DataType: "integer", Default: &old}}}}, `{"tables":[{"name":"users","columns":[{"name":"id","type":"integer","default":null}]}]}`)
	if !strings.Contains(sql, "DROP DEFAULT") {
		t.Fatalf("default removal not generated: %s", sql)
	}
}

func TestGenerateMigrationSQLReplacesPrimaryKeyInOrder(t *testing.T) {
	_, sql := compareAndGenerateSQL(t, "postgres", []database.Table{{Schema: "public", Name: "users", Columns: []database.Column{{Name: "id", DataType: "integer"}, {Name: "other", DataType: "integer"}}, Constraints: []database.Constraint{{Name: "old_pk", Type: "PRIMARY KEY", Columns: []string{"id"}}}}}, `{"tables":[{"name":"users","columns":[{"name":"id","type":"integer"},{"name":"other","type":"integer"}],"constraints":[{"name":"new_pk","type":"PRIMARY KEY","columns":["other"]}]}]}`)
	drop, add := strings.Index(sql, "DROP CONSTRAINT"), strings.Index(sql, "ADD CONSTRAINT")
	if drop < 0 || add < 0 || drop > add {
		t.Fatalf("replacement PK must remove old PK before adding new PK: %s", sql)
	}
}

func TestGenerateMigrationSQLCreatesTablesBeforeForeignKeys(t *testing.T) {
	_, sql := compareAndGenerateSQL(t, "postgres", nil, `{"tables":[{"name":"a_child","columns":[{"name":"parent_id","type":"integer"}],"constraints":[{"name":"child_parent","type":"FOREIGN KEY","columns":["parent_id"],"definition":"FOREIGN KEY (parent_id) REFERENCES public.z_parent(id)"}]},{"name":"z_parent","columns":[{"name":"id","type":"integer"}],"primary_key":["id"]}]}`)
	parent, fk := strings.Index(sql, `CREATE TABLE "public"."z_parent"`), strings.Index(sql, `ADD CONSTRAINT "child_parent"`)
	if parent < 0 || fk < 0 || fk < parent {
		t.Fatalf("new-table foreign key must follow creation of referenced parent: %s", sql)
	}
}

func TestGenerateMigrationSQLDropsForeignKeysBeforeTables(t *testing.T) {
	_, sql := compareAndGenerateSQL(t, "postgres", []database.Table{
		{Schema: "public", Name: "a_parent", Columns: []database.Column{{Name: "id", DataType: "integer"}}, Constraints: []database.Constraint{{Name: "a_parent_pkey", Type: "PRIMARY KEY", Columns: []string{"id"}}}},
		{Schema: "public", Name: "z_child", Columns: []database.Column{{Name: "id", DataType: "integer"}}, Constraints: []database.Constraint{{Name: "child_parent", Type: "FOREIGN KEY", Columns: []string{"id"}, Definition: `FOREIGN KEY (id) REFERENCES public.a_parent(id)`}}},
	}, `{"tables":[{"name":"retained","columns":[{"name":"id","type":"integer"}]}]}`)
	dropFK, dropParent, dropChild := strings.Index(sql, `DROP CONSTRAINT "child_parent"`), strings.Index(sql, `DROP TABLE "public"."a_parent"`), strings.Index(sql, `DROP TABLE "public"."z_child"`)
	if dropParent < 0 || dropChild < 0 || (dropChild > dropParent && (dropFK < 0 || dropFK > dropParent)) {
		t.Fatalf("referencing child or its FK must be dropped before parent: %s", sql)
	}
}

func TestGenerateMigrationSQLRemovesExplicitNullDefault(t *testing.T) {
	old := "7"
	_, sql := compareAndGenerateSQL(t, "postgres", []database.Table{{Schema: "public", Name: "users", Columns: []database.Column{{Name: "id", DataType: "integer", Default: &old}}}}, `{"tables":[{"name":"users","columns":[{"name":"id","type":"integer","default":null}]}]}`)
	if !strings.Contains(sql, "DROP DEFAULT") {
		t.Fatalf("explicit null must remove default: %s", sql)
	}
}

func TestGenerateMigrationSQLRejectsMigrationReceipts(t *testing.T) {
	sql, err := GenerateMigrationSQL(&DiffResult{DBMS: "postgres", Changes: []SchemaChange{{Action: ActionDropTable, Schema: "public", Table: "_dbplatform_migration_receipts"}}}, "postgres")
	if err == nil || sql != "" {
		t.Fatalf("generator must reject reserved table: %q %v", sql, err)
	}
}

func TestGenerateMigrationSQLRejectsInvalidDiff(t *testing.T) {
	for name, diff := range map[string]*DiffResult{
		"mismatched dialect": {DBMS: "mysql", Changes: []SchemaChange{{Action: ActionDropTable, Schema: "public", Table: "users"}}},
		"unsupported action": {DBMS: "postgres", Changes: []SchemaChange{{Action: "UNSUPPORTED", Schema: "public", Table: "users"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if sql, err := GenerateMigrationSQL(diff, "postgres"); err == nil || sql != "" {
				t.Fatalf("invalid diff must not return partial success: %q %v", sql, err)
			}
		})
	}
}

func TestGenerateCandidateSQL(t *testing.T) {
	defaultPrice := "0.00"
	diff := &DiffResult{
		IsIdentical: false,
		Changes: []SchemaChange{
			{
				Action: ActionCreateTable, Schema: "public", Table: "products",
				Details: TableDef{
					Schema: "public", Name: "products",
					Columns: []ColumnDef{
						{Name: "id", DataType: "int", Nullable: false},
						{Name: "price", DataType: "numeric(10,2)", Nullable: false, Default: &defaultPrice},
					},
					PrimaryKey: []string{"id"},
				},
			},
			{
				Action: ActionAddColumn, Schema: "public", Table: "users", Object: "phone",
				Details: ColumnDef{
					Name: "phone", DataType: "varchar(20)", Nullable: true,
				},
			},
			{
				Action: ActionModifyColumn, Schema: "public", Table: "users", Object: "bio",
				Details: ColumnDef{
					Name: "bio", DataType: "text", Nullable: false,
				},
			},
			{
				Action: ActionDropColumn, Schema: "public", Table: "users", Object: "temp_token",
				Destructive: true,
			},
			{
				Action: ActionCreateIndex, Schema: "public", Table: "users", Object: "idx_phone",
				Details: IndexDef{
					Name: "idx_phone", Columns: []string{"phone"}, Unique: false,
				},
			},
			{
				Action: ActionDropIndex, Schema: "public", Table: "users", Object: "idx_old_bio",
				Destructive: true,
			},
			{
				Action: ActionDropTable, Schema: "public", Table: "old_logs",
				Destructive: true,
			},
		},
	}

	// 1. PostgreSQL Candidate SQL
	pgSQL, err := GenerateMigrationSQL(diff, "postgres")
	if err != nil {
		t.Fatalf("unexpected error generating PG SQL: %v", err)
	}
	expectedPG := []string{
		`CREATE TABLE "public"."products"`,
		`ALTER TABLE "public"."users" ADD COLUMN "phone" varchar(20);`,
		`ALTER TABLE "public"."users" ALTER COLUMN "bio" TYPE text;`,
		`ALTER TABLE "public"."users" ALTER COLUMN "bio" SET NOT NULL;`,
		`ALTER TABLE "public"."users" DROP COLUMN "temp_token";`,
		`CREATE INDEX "idx_phone" ON "public"."users" ("phone");`,
		`DROP INDEX "public"."idx_old_bio";`,
		`DROP TABLE "public"."old_logs";`,
	}
	for _, expected := range expectedPG {
		if !strings.Contains(pgSQL, expected) {
			t.Errorf("PG SQL missing expected statement: %s\nFull SQL:\n%s", expected, pgSQL)
		}
	}

	// 2. MySQL Candidate SQL
	mySQL, err := GenerateMigrationSQL(diff, "mysql")
	if err != nil {
		t.Fatalf("unexpected error generating MySQL SQL: %v", err)
	}
	expectedMySQL := []string{
		"CREATE TABLE `public`.`products`",
		"ALTER TABLE `public`.`users` ADD COLUMN `phone` varchar(20);",
		"ALTER TABLE `public`.`users` MODIFY COLUMN `bio` text NOT NULL;",
		"ALTER TABLE `public`.`users` DROP COLUMN `temp_token`;",
		"CREATE INDEX `idx_phone` ON `public`.`users` (`phone`);",
		"DROP INDEX `idx_old_bio` ON `public`.`users`;",
		"DROP TABLE `public`.`old_logs`;",
	}
	for _, expected := range expectedMySQL {
		if !strings.Contains(mySQL, expected) {
			t.Errorf("MySQL SQL missing expected statement: %s\nFull SQL:\n%s", expected, mySQL)
		}
	}

	// 3. Unsupported DBMS
	if _, err := GenerateMigrationSQL(diff, "sqlite"); err == nil {
		t.Error("expected error for unsupported DBMS 'sqlite'")
	}

	// 4. HasDestructiveChanges helper verification
	if !HasDestructiveChanges(diff.Changes) {
		t.Error("expected HasDestructiveChanges to return true")
	}
	nonDestructiveChanges := []SchemaChange{
		{Action: ActionAddColumn, Destructive: false},
	}
	if HasDestructiveChanges(nonDestructiveChanges) {
		t.Error("expected HasDestructiveChanges to return false for non-destructive changes")
	}
}
