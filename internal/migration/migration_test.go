package migration

import (
	"errors"
	"strings"
	"testing"
)

func TestMigrationSQLValidation(t *testing.T) {
	for _, tc := range []struct {
		dbms, query string
		count       int
	}{
		{"postgres", "CREATE TABLE x(id int); INSERT INTO x VALUES (1);", 2},
		{"mysql", "INSERT INTO x VALUES ('a;b', 'it''s'); -- end\n UPDATE x SET id=2", 2},
		{"postgres", "CREATE FUNCTION f() RETURNS text AS $body$ SELECT ';'; $body$ LANGUAGE SQL;", 1},
		{"postgres", `/* outer /* inner ; */ end */ SELECT E'a\';b'; SELECT 2`, 2},
		{"mysql", "# comment ;\n SELECT `semi;colon` FROM x;", 1},
	} {
		t.Run(tc.dbms+tc.query, func(t *testing.T) {
			statements, err := ValidateSQL(tc.dbms, tc.query)
			if err != nil || len(statements) != tc.count {
				t.Fatalf("statements=%v error=%v", statements, err)
			}
		})
	}
	for _, query := range []string{"", " ; -- nothing", "SELECT 'unterminated", "SELECT 1; COMMIT", "BEGIN; SELECT 1", "SET autocommit=1", "USE other", "SELECT 1; /* unfinished", "SELECT * FROM _dbplatform_migration_receipts", "/*!50000 COMMIT */", strings.Repeat("x", MaxSQLBytes+1)} {
		if _, err := ValidateSQL("mysql", query); err == nil {
			t.Errorf("accepted unsafe/invalid query %q", query)
		}
	}
	if _, err := ValidateSQL("sqlite", "SELECT 1"); err == nil {
		t.Fatal("unsupported DBMS accepted")
	}
}

func TestSanitizeMigrationError(t *testing.T) {
	secret := errors.New("postgres://alice:PrivatePassword@secret-host/db SQLSTATE 28P01")
	got := executionError(secret)
	for _, value := range []string{"alice", "PrivatePassword", "secret-host", "SQLSTATE"} {
		if strings.Contains(got, value) {
			t.Fatalf("secret leaked: %s", got)
		}
	}
	if got == "" {
		t.Fatal("missing safe error")
	}
}
