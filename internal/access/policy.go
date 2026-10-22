package access

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// AccessLevel defines the privilege level on a target database.
type AccessLevel string

const (
	AccessNone     AccessLevel = "NONE"
	AccessViewOnly AccessLevel = "VIEW_ONLY"
	AccessViewEdit AccessLevel = "VIEW_EDIT"
)

// ParseAccessLevel normalizes strings to AccessLevel.
func ParseAccessLevel(s string) AccessLevel {
	normalized := strings.ToUpper(strings.TrimSpace(s))
	switch normalized {
	case "VIEW_ONLY", "VIEW ONLY", "VIEWONLY":
		return AccessViewOnly
	case "VIEW_EDIT", "VIEW + EDIT", "VIEW+EDIT", "VIEWEDIT", "EDIT":
		return AccessViewEdit
	default:
		return AccessNone
	}
}

// Action represents an operation performed on a target database.
type Action string

const (
	ActionViewSchema       Action = "VIEW_SCHEMA"
	ActionViewData         Action = "VIEW_DATA"
	ActionViewHistory      Action = "VIEW_HISTORY"
	ActionCreateMigration  Action = "CREATE_MIGRATION"
	ActionExecuteMigration Action = "EXECUTE_MIGRATION"
)

// RequiredLevel returns the minimum AccessLevel needed to perform the given Action.
func RequiredLevel(action Action) AccessLevel {
	switch action {
	case ActionViewSchema, ActionViewData, ActionViewHistory:
		return AccessViewOnly
	case ActionCreateMigration, ActionExecuteMigration:
		return AccessViewEdit
	default:
		return AccessViewEdit
	}
}

// IsHigherOrEqual checks if currentLevel satisfies the requiredLevel.
// Hierarchy: NONE (0) < VIEW_ONLY (1) < VIEW_EDIT (2)
func IsHigherOrEqual(currentLevel, requiredLevel AccessLevel) bool {
	rank := func(lvl AccessLevel) int {
		switch lvl {
		case AccessViewEdit:
			return 2
		case AccessViewOnly:
			return 1
		default:
			return 0
		}
	}
	return rank(currentLevel) >= rank(requiredLevel)
}

// CalculateEffectiveAccess resolves the highest access level from a list of grant levels.
// Used for pure logic evaluation in unit tests.
func CalculateEffectiveAccess(levels []AccessLevel) AccessLevel {
	highest := AccessNone
	for _, lvl := range levels {
		if lvl == AccessViewEdit {
			return AccessViewEdit
		}
		if lvl == AccessViewOnly {
			highest = AccessViewOnly
		}
	}
	return highest
}

// CanPerformAction checks whether an access level permits an action.
func CanPerformAction(level AccessLevel, action Action) bool {
	required := RequiredLevel(action)
	return IsHigherOrEqual(level, required)
}

// GetEffectiveAccess calculates the user's real-time effective access level on a given target database
// by aggregating across all groups the user belongs to.
func GetEffectiveAccess(ctx context.Context, db *sql.DB, userID, databaseID string) (AccessLevel, error) {
	if userID == "" || databaseID == "" {
		return AccessNone, nil
	}

	query := `
		SELECT gdg.access_level
		FROM group_database_grants gdg
		INNER JOIN user_groups ug ON gdg.group_id = ug.group_id
		WHERE ug.user_id = $1 AND gdg.database_id = $2
	`

	rows, err := db.QueryContext(ctx, query, userID, databaseID)
	if err != nil {
		return AccessNone, fmt.Errorf("failed to query database grants for user: %w", err)
	}
	defer rows.Close()

	var levels []AccessLevel
	for rows.Next() {
		var rawLevel string
		if err := rows.Scan(&rawLevel); err != nil {
			return AccessNone, fmt.Errorf("failed to scan access level: %w", err)
		}
		levels = append(levels, ParseAccessLevel(rawLevel))
	}

	if err := rows.Err(); err != nil {
		return AccessNone, fmt.Errorf("error during grants iteration: %w", err)
	}

	return CalculateEffectiveAccess(levels), nil
}
