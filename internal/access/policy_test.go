package access_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/access"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/config"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
)

// Unit Test: TestEffectiveAccessCalculation tests pure multi-group permission aggregation logic.
func TestEffectiveAccessCalculation(t *testing.T) {
	tests := []struct {
		name     string
		levels   []access.AccessLevel
		expected access.AccessLevel
	}{
		{
			name:     "User with no groups",
			levels:   []access.AccessLevel{},
			expected: access.AccessNone,
		},
		{
			name:     "User with only NONE grants",
			levels:   []access.AccessLevel{access.AccessNone},
			expected: access.AccessNone,
		},
		{
			name:     "User in single group with View Only",
			levels:   []access.AccessLevel{access.AccessViewOnly},
			expected: access.AccessViewOnly,
		},
		{
			name:     "User in single group with View + Edit",
			levels:   []access.AccessLevel{access.AccessViewEdit},
			expected: access.AccessViewEdit,
		},
		{
			name:     "User in multiple groups: View Only and View + Edit",
			levels:   []access.AccessLevel{access.AccessViewOnly, access.AccessViewEdit},
			expected: access.AccessViewEdit,
		},
		{
			name:     "User in multiple groups: NONE and View Only",
			levels:   []access.AccessLevel{access.AccessNone, access.AccessViewOnly},
			expected: access.AccessViewOnly,
		},
		{
			name:     "User in multiple groups: View + Edit, View Only, and NONE",
			levels:   []access.AccessLevel{access.AccessViewEdit, access.AccessViewOnly, access.AccessNone},
			expected: access.AccessViewEdit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := access.CalculateEffectiveAccess(tt.levels)
			if got != tt.expected {
				t.Errorf("CalculateEffectiveAccess() = %v, expected %v", got, tt.expected)
			}
		})
	}
}

// Helper to get connected test database
func setupIntegrationDB(t *testing.T) *sql.DB {
	if os.Getenv("WBD_TEST_DATABASES") != "1" {
		t.Skip("use scripts/test-integration.ps1 with dedicated test databases")
	}
	os.Setenv("ENCRYPTION_KEY", "12345678901234567890123456789012")
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("Skipping integration test: config load failed: %v", err)
	}

	db, err := database.NewPlatformDB(cfg)
	if err != nil {
		t.Skipf("Skipping integration test: database connection failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := database.MigratePlatformDB(ctx, db); err != nil {
		t.Fatalf("Failed to migrate test database: %v", err)
	}

	// Clean tables
	_, _ = db.ExecContext(ctx, "TRUNCATE sessions, user_groups, group_database_grants, migrations, database_connections, groups, users CASCADE")

	return db
}

// Integration Test: TestRealTimeGroupMembershipUpdate verifies real-time permission evaluation.
func TestRealTimeGroupMembershipUpdate(t *testing.T) {
	db := setupIntegrationDB(t)
	defer db.Close()

	ctx := context.Background()

	// 1. Create User
	userID := uuid.New().String()
	_, err := db.ExecContext(ctx, "INSERT INTO users (id, username, password_hash) VALUES ($1, 'testuser', 'hash')", userID)
	if err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	// 2. Create Target Database Connection
	dbID := uuid.New().String()
	_, err = db.ExecContext(ctx, `
		INSERT INTO database_connections (id, name, dbms_type, host, port, db_name, username, password_encrypted)
		VALUES ($1, 'Production PG', 'postgres', '127.0.0.1', 5432, 'prod_db', 'postgres', 'enc')
	`, dbID)
	if err != nil {
		t.Fatalf("Failed to create test db connection: %v", err)
	}

	// 3. Create Group A (View Only) and Group B (View + Edit)
	groupAID := uuid.New().String()
	_, err = db.ExecContext(ctx, "INSERT INTO groups (id, name) VALUES ($1, 'Auditors')", groupAID)
	if err != nil {
		t.Fatalf("Failed to create Group A: %v", err)
	}
	_, err = db.ExecContext(ctx, "INSERT INTO group_database_grants (group_id, database_id, access_level) VALUES ($1, $2, 'VIEW_ONLY')", groupAID, dbID)
	if err != nil {
		t.Fatalf("Failed to assign grant to Group A: %v", err)
	}

	groupBID := uuid.New().String()
	_, err = db.ExecContext(ctx, "INSERT INTO groups (id, name) VALUES ($1, 'Developers')", groupBID)
	if err != nil {
		t.Fatalf("Failed to create Group B: %v", err)
	}
	_, err = db.ExecContext(ctx, "INSERT INTO group_database_grants (group_id, database_id, access_level) VALUES ($1, $2, 'VIEW_EDIT')", groupBID, dbID)
	if err != nil {
		t.Fatalf("Failed to assign grant to Group B: %v", err)
	}

	// Step A: User has no groups -> should have NONE
	level, err := access.GetEffectiveAccess(ctx, db, userID, dbID)
	if err != nil {
		t.Fatalf("GetEffectiveAccess error: %v", err)
	}
	if level != access.AccessNone {
		t.Errorf("Expected NONE for user without groups, got %s", level)
	}

	// Step B: Add user to Group A -> real-time check should return VIEW_ONLY
	_, err = db.ExecContext(ctx, "INSERT INTO user_groups (user_id, group_id) VALUES ($1, $2)", userID, groupAID)
	if err != nil {
		t.Fatalf("Failed to add user to Group A: %v", err)
	}
	level, err = access.GetEffectiveAccess(ctx, db, userID, dbID)
	if err != nil {
		t.Fatalf("GetEffectiveAccess error: %v", err)
	}
	if level != access.AccessViewOnly {
		t.Errorf("Expected VIEW_ONLY after joining Group A, got %s", level)
	}

	// Step C: Add user to Group B -> real-time check should immediately return VIEW_EDIT
	_, err = db.ExecContext(ctx, "INSERT INTO user_groups (user_id, group_id) VALUES ($1, $2)", userID, groupBID)
	if err != nil {
		t.Fatalf("Failed to add user to Group B: %v", err)
	}
	level, err = access.GetEffectiveAccess(ctx, db, userID, dbID)
	if err != nil {
		t.Fatalf("GetEffectiveAccess error: %v", err)
	}
	if level != access.AccessViewEdit {
		t.Errorf("Expected VIEW_EDIT after joining Group B, got %s", level)
	}

	// Step D: Remove user from Group B -> real-time check should immediately fall back to VIEW_ONLY
	_, err = db.ExecContext(ctx, "DELETE FROM user_groups WHERE user_id = $1 AND group_id = $2", userID, groupBID)
	if err != nil {
		t.Fatalf("Failed to remove user from Group B: %v", err)
	}
	level, err = access.GetEffectiveAccess(ctx, db, userID, dbID)
	if err != nil {
		t.Fatalf("GetEffectiveAccess error: %v", err)
	}
	if level != access.AccessViewOnly {
		t.Errorf("Expected VIEW_ONLY after leaving Group B, got %s", level)
	}

	// Step E: Remove user from Group A -> real-time check should return NONE
	_, err = db.ExecContext(ctx, "DELETE FROM user_groups WHERE user_id = $1 AND group_id = $2", userID, groupAID)
	if err != nil {
		t.Fatalf("Failed to remove user from Group A: %v", err)
	}
	level, err = access.GetEffectiveAccess(ctx, db, userID, dbID)
	if err != nil {
		t.Fatalf("GetEffectiveAccess error: %v", err)
	}
	if level != access.AccessNone {
		t.Errorf("Expected NONE after leaving all groups, got %s", level)
	}
}
