package database

import "testing"

func TestMySQLPrivilegeBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, db string
		grants   []string
		allowed  bool
	}{
		{"scoped", "target", []string{"GRANT USAGE ON *.* TO `u`@`%`", "GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, REFERENCES, INDEX, ALTER, SHOW VIEW, CREATE VIEW, TRIGGER ON `target`.* TO `u`@`%`"}, true},
		{"read_and_inspect", "target", []string{"GRANT SELECT, SHOW VIEW, TRIGGER ON `target`.* TO `u`@`%`"}, true},
		{"literal_underscore", "target_db", []string{"GRANT SELECT, SHOW VIEW, TRIGGER ON `target\\_db`.* TO `u`@`%`"}, true},
		{"wildcard_underscore", "target_db", []string{"GRANT SELECT ON `target_db`.* TO `u`@`%`"}, false},
		{"wildcard_percent", "target%", []string{"GRANT SELECT ON `target%`.* TO `u`@`%`"}, false},
		{"global", "target", []string{"GRANT SELECT ON *.* TO `u`@`%`"}, false},
		{"other_db", "target", []string{"GRANT SELECT ON `target`.* TO `u`@`%`", "GRANT SELECT ON `other`.* TO `u`@`%`"}, false},
		{"routine", "target", []string{"GRANT SELECT, EXECUTE ON `target`.* TO `u`@`%`"}, false},
		{"grant_option", "target", []string{"GRANT SELECT ON `target`.* TO `u`@`%` WITH GRANT OPTION"}, false},
		{"role", "target", []string{"GRANT `role`@`%` TO `u`@`%`"}, false},
		{"no_privileges", "target", []string{"GRANT USAGE ON *.* TO `u`@`%`"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateMySQLGrants(tc.db, tc.grants); (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v, error=%v", tc.allowed, err)
			}
		})
	}
}
