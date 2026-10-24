package database

import "testing"

func TestMySQLScopeRequiresObjectInspectionPrivileges(t *testing.T) {
	for _, grant := range []string{"SELECT", "SELECT, SHOW VIEW", "SELECT, TRIGGER"} {
		if err := validateMySQLGrants("target", []string{"GRANT " + grant + " ON `target`.* TO `u`@`%`"}); err == nil {
			t.Errorf("accepted account unable to inspect all stored objects: %s", grant)
		}
	}
}
