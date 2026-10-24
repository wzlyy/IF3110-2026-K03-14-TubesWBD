package migration

import (
	"strings"
	"testing"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
)

func TestParseTargetSchemaJSON(t *testing.T) {
	// Case 1: Empty input
	_, err := ParseTargetSchemaJSON([]byte(""), "public")
	if err == nil {
		t.Error("expected error for empty schema input")
	}

	// Case 2: Invalid JSON
	_, err = ParseTargetSchemaJSON([]byte("{not a json}"), "public")
	if err == nil {
		t.Error("expected error for invalid JSON")
	}

	// Case 3: Valid flat tables format
	flatJSON := `{
		"dbms": "postgres",
		"tables": [
			{
				"name": "users",
				"columns": [
					{"name": "id", "type": "int", "nullable": false},
					{"name": "name", "type": "varchar(255)", "nullable": false}
				],
				"primary_key": ["id"]
			}
		]
	}`
	def, err := ParseTargetSchemaJSON([]byte(flatJSON), "public")
	if err != nil {
		t.Fatalf("unexpected error parsing flat tables: %v", err)
	}
	if len(def.Schemas) != 1 || def.Schemas[0].Name != "public" {
		t.Fatalf("expected 1 schema 'public', got %v", def.Schemas)
	}
	if len(def.Schemas[0].Tables) != 1 || def.Schemas[0].Tables[0].Name != "users" {
		t.Fatalf("expected table 'users', got %v", def.Schemas[0].Tables)
	}
}

func TestSchemaDiffComparison(t *testing.T) {
	// Base catalog with one table "users"
	curCatalog := &database.Catalog{
		DBMS: "postgres",
		Name: "testdb",
		Schemas: []database.Schema{
			{
				Name: "public",
				Tables: []database.Table{
					{
						Schema: "public",
						Name:   "users",
						Columns: []database.Column{
							{Name: "id", DataType: "integer", Nullable: false},
							{Name: "username", DataType: "varchar(50)", Nullable: false},
							{Name: "obsolete_col", DataType: "text", Nullable: true},
						},
						Constraints: []database.Constraint{
							{Name: "users_pkey", Type: "PRIMARY KEY", Columns: []string{"id"}},
						},
						Indexes: []database.Index{
							{Name: "idx_users_id", Columns: []string{"id"}, Primary: true},
							{Name: "idx_old", Columns: []string{"obsolete_col"}, Unique: false},
						},
					},
					{
						Schema: "public",
						Name:   "dropped_tbl",
						Columns: []database.Column{
							{Name: "id", DataType: "integer", Nullable: false},
						},
					},
				},
			},
		},
	}

	// Desired schema:
	// 1. "users":
	//    - "id" unchanged
	//    - "username" modified to varchar(100)
	//    - "obsolete_col" dropped
	//    - "email" added
	//    - "idx_old" dropped
	//    - "idx_users_email" created
	// 2. "dropped_tbl" dropped
	// 3. "orders" created
	targetJSON := `{
		"dbms": "postgres",
		"tables": [
			{
				"schema": "public",
				"name": "users",
				"columns": [
					{"name": "id", "type": "integer", "nullable": false},
					{"name": "username", "type": "varchar(100)", "nullable": false},
					{"name": "email", "type": "varchar(255)", "nullable": false}
				],
				"primary_key": ["id"],
				"indexes": [
					{"name": "idx_users_email", "columns": ["email"], "unique": true}
				]
			},
			{
				"schema": "public",
				"name": "orders",
				"columns": [
					{"name": "id", "type": "integer", "nullable": false},
					{"name": "user_id", "type": "integer", "nullable": false},
					{"name": "amount", "type": "numeric(10,2)", "nullable": false}
				],
				"primary_key": ["id"]
			}
		]
	}`

	des, err := ParseTargetSchemaJSON([]byte(targetJSON), "public")
	if err != nil {
		t.Fatalf("failed to parse desired schema: %v", err)
	}

	diff, err := CompareCatalogs(curCatalog, des)
	if err != nil {
		t.Fatalf("failed to compare catalogs: %v", err)
	}

	if diff.IsIdentical {
		t.Fatal("diff should not be identical")
	}

	actions := make(map[DiffAction]int)
	for _, ch := range diff.Changes {
		actions[ch.Action]++
	}

	// Verify expected changes
	if actions[ActionCreateTable] != 1 {
		t.Errorf("expected 1 CREATE_TABLE (orders), got %d", actions[ActionCreateTable])
	}
	if actions[ActionDropTable] != 1 {
		t.Errorf("expected 1 DROP_TABLE (dropped_tbl), got %d", actions[ActionDropTable])
	}
	if actions[ActionAddColumn] != 1 {
		t.Errorf("expected 1 ADD_COLUMN (email), got %d", actions[ActionAddColumn])
	}
	if actions[ActionDropColumn] != 1 {
		t.Errorf("expected 1 DROP_COLUMN (obsolete_col), got %d", actions[ActionDropColumn])
	}
	if actions[ActionModifyColumn] != 1 {
		t.Errorf("expected 1 MODIFY_COLUMN (username), got %d", actions[ActionModifyColumn])
	}
	if actions[ActionCreateIndex] != 1 {
		t.Errorf("expected 1 CREATE_INDEX (idx_users_email), got %d", actions[ActionCreateIndex])
	}
	if actions[ActionDropIndex] != 1 {
		t.Errorf("expected 1 DROP_INDEX (idx_old), got %d", actions[ActionDropIndex])
	}

	// Verify SQL Generation for PostgreSQL
	pgSQL, err := GenerateMigrationSQL(diff, "postgres")
	if err != nil {
		t.Fatalf("failed to generate postgres SQL: %v", err)
	}

	if !strings.Contains(pgSQL, `CREATE TABLE "public"."orders"`) {
		t.Error("expected CREATE TABLE \"public\".\"orders\" in PostgreSQL SQL")
	}
	if !strings.Contains(pgSQL, `ALTER TABLE "public"."users" ADD COLUMN "email" varchar(255) NOT NULL;`) {
		t.Error("expected ADD COLUMN \"email\" in PostgreSQL SQL")
	}
	if !strings.Contains(pgSQL, `ALTER TABLE "public"."users" ALTER COLUMN "username" TYPE varchar(100);`) {
		t.Error("expected ALTER COLUMN \"username\" in PostgreSQL SQL")
	}
	if !strings.Contains(pgSQL, `DROP TABLE "public"."dropped_tbl";`) {
		t.Error("expected DROP TABLE \"public\".\"dropped_tbl\" in PostgreSQL SQL")
	}
	hasDestructive := false
	for _, ch := range diff.Changes {
		if ch.Destructive {
			hasDestructive = true
			break
		}
	}
	if !hasDestructive {
		t.Error("expected destructive changes flagged in diff result")
	}

	// Verify SQL Generation for MySQL
	mySQL, err := GenerateMigrationSQL(diff, "mysql")
	if err != nil {
		t.Fatalf("failed to generate MySQL SQL: %v", err)
	}

	if !strings.Contains(mySQL, "CREATE TABLE `public`.`orders`") {
		t.Error("expected CREATE TABLE `public`.`orders` in MySQL SQL")
	}
	if !strings.Contains(mySQL, "ALTER TABLE `public`.`users` ADD COLUMN `email` varchar(255) NOT NULL;") {
		t.Error("expected ADD COLUMN `email` in MySQL SQL")
	}
	if !strings.Contains(mySQL, "ALTER TABLE `public`.`users` MODIFY COLUMN `username` varchar(100) NOT NULL;") {
		t.Error("expected MODIFY COLUMN `username` in MySQL SQL")
	}
	if !strings.Contains(mySQL, "DROP TABLE `public`.`dropped_tbl`;") {
		t.Error("expected DROP TABLE `public`.`dropped_tbl` in MySQL SQL")
	}
}

func TestSchemaDiffIdentical(t *testing.T) {
	cat := &database.Catalog{
		DBMS: "postgres",
		Name: "testdb",
		Schemas: []database.Schema{
			{
				Name: "public",
				Tables: []database.Table{
					{
						Schema: "public",
						Name:   "users",
						Columns: []database.Column{
							{Name: "id", DataType: "integer", Nullable: false},
							{Name: "name", DataType: "varchar(100)", Nullable: false},
						},
						Constraints: []database.Constraint{
							{Name: "users_pkey", Type: "PRIMARY KEY", Columns: []string{"id"}},
						},
					},
				},
			},
		},
	}

	targetJSON := `{
		"dbms": "postgres",
		"tables": [
			{
				"schema": "public",
				"name": "users",
				"columns": [
					{"name": "id", "type": "integer", "nullable": false},
					{"name": "name", "type": "varchar(100)", "nullable": false}
				],
				"primary_key": ["id"]
			}
		]
	}`

	des, err := ParseTargetSchemaJSON([]byte(targetJSON), "public")
	if err != nil {
		t.Fatal(err)
	}

	diff, err := CompareCatalogs(cat, des)
	if err != nil {
		t.Fatal(err)
	}

	if !diff.IsIdentical || len(diff.Changes) != 0 {
		t.Fatalf("expected identical schemas, got %d changes", len(diff.Changes))
	}

	sql, err := GenerateMigrationSQL(diff, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if sql != "" {
		t.Errorf("expected empty SQL string for identical schemas, got: %s", sql)
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
