package database

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

var ErrUnsafeMySQLPrivileges = errors.New("Akun MySQL harus dibatasi ke database tujuan saja, tanpa privilege global, role, routine, atau GRANT OPTION. Periksa provisioning akun target.")
var ErrUnsafeMySQLObjects = errors.New("Objek MySQL tidak aman untuk migration: gunakan view SQL SECURITY INVOKER atau definer akun target yang dibatasi, serta definer trigger akun target yang sama.")

// mysqlGrantScope quotes an EXACT database grant, not a LIKE-style pattern.
// Quoting alone does not disable MySQL's _/% wildcards in database grants.
func mysqlGrantScope(name string) (string, error) {
	pattern := strings.NewReplacer("\\", "\\\\", "_", "\\_", "%", "\\%").Replace(name)
	return QuoteIdentifier("mysql", pattern)
}

func validateMySQLGrants(name string, grants []string) error {
	scope, err := mysqlGrantScope(name)
	if err != nil {
		return ErrUnsafeMySQLPrivileges
	}
	scoped := false
	showView, trigger := false, false
	for _, grant := range grants {
		if strings.Contains(grant, " WITH GRANT OPTION") || !strings.HasPrefix(grant, "GRANT ") {
			return ErrUnsafeMySQLPrivileges
		}
		parts := strings.SplitN(strings.TrimPrefix(grant, "GRANT "), " ON ", 2)
		if len(parts) != 2 {
			return ErrUnsafeMySQLPrivileges
		} // Includes role grants.
		if parts[0] == "USAGE" && strings.HasPrefix(parts[1], "*.* TO ") {
			continue
		}
		if !strings.HasPrefix(parts[1], scope+".* TO ") {
			return ErrUnsafeMySQLPrivileges
		}
		for _, privilege := range strings.Split(parts[0], ", ") {
			switch privilege {
			case "SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "REFERENCES", "INDEX", "ALTER", "CREATE VIEW", "SHOW VIEW", "TRIGGER":
			default:
				return ErrUnsafeMySQLPrivileges
			}
			showView = showView || privilege == "SHOW VIEW"
			trigger = trigger || privilege == "TRIGGER"
		}
		scoped = true
	}
	if !scoped || !showView || !trigger {
		return ErrUnsafeMySQLPrivileges
	}
	return nil
}

// ValidateMySQLScope checks current server grants on EVERY free-form SQL request.
// Database credentials enforce the boundary for qualified names and nested SQL;
// parsing a user's SQL is not an authorization boundary. No grants are cached.
func ValidateMySQLScope(ctx context.Context, target *sql.DB, name string) error {
	var role, account string
	if err := target.QueryRowContext(ctx, "SELECT CURRENT_ROLE(),CURRENT_USER()").Scan(&role, &account); err != nil || role != "NONE" {
		return ErrUnsafeMySQLPrivileges
	}
	rows, err := target.QueryContext(ctx, "SHOW GRANTS FOR CURRENT_USER")
	if err != nil {
		return ErrUnsafeMySQLPrivileges
	}
	defer rows.Close()
	var grants []string
	for rows.Next() {
		var grant string
		if err := rows.Scan(&grant); err != nil {
			return ErrUnsafeMySQLPrivileges
		}
		grants = append(grants, grant)
	}
	if rows.Err() != nil {
		return ErrUnsafeMySQLPrivileges
	}
	if err := rows.Close(); err != nil {
		return ErrUnsafeMySQLPrivileges
	}
	if err := validateMySQLGrants(name, grants); err != nil {
		return err
	}
	// SHOW VIEW and TRIGGER are mandatory so hidden metadata cannot be mistaken
	// for absence. Invoker views and same-account definers preserve the boundary.
	var unsafeViews, unsafeTriggers int
	if err := target.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.TABLES t
	 LEFT JOIN information_schema.VIEWS v ON v.TABLE_SCHEMA=t.TABLE_SCHEMA AND v.TABLE_NAME=t.TABLE_NAME
	 WHERE t.TABLE_SCHEMA=? AND t.TABLE_TYPE='VIEW'
	 AND (v.TABLE_NAME IS NULL OR (v.SECURITY_TYPE<>'INVOKER' AND v.DEFINER<>?))`, name, account).Scan(&unsafeViews); err != nil {
		return ErrUnsafeMySQLObjects
	}
	if err := target.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA=? AND DEFINER<>?`, name, account).Scan(&unsafeTriggers); err != nil {
		return ErrUnsafeMySQLObjects
	}
	if unsafeViews != 0 || unsafeTriggers != 0 {
		return ErrUnsafeMySQLObjects
	}
	return nil
}
