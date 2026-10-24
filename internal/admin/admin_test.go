package admin_test

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/admin"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/config"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
)

type mockRenderer struct{}

func (m *mockRenderer) ExecuteTemplate(w io.Writer, name string, data any) error {
	return nil
}

func setupTestDB(t *testing.T) *sql.DB {
	if os.Getenv("WBD_TEST_DATABASES") != "1" {
		t.Skip("use scripts/test-integration.ps1 with dedicated test databases")
	}
	os.Setenv("ENCRYPTION_KEY", "12345678901234567890123456789012")
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("Skipping test: config load error: %v", err)
	}

	db, err := database.NewPlatformDB(cfg)
	if err != nil {
		t.Skipf("Skipping test: DB connect error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = database.MigratePlatformDB(ctx, db)
	_, _ = db.ExecContext(ctx, "TRUNCATE sessions, user_groups, group_database_grants, migrations, database_connections, groups, users CASCADE")

	return db
}

// Integration Test 1: TestAdminAuthorizationEnforcement
// Verifies that standard users are strictly denied (HTTP 403) from accessing admin endpoints.
func TestAdminAuthorizationEnforcement(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()

	// 1. Create a regular (non-admin) user
	regularUserID := uuid.New().String()
	_, _ = db.ExecContext(ctx, "INSERT INTO users (id, username, password_hash, is_platform_admin, is_active) VALUES ($1, 'regular_user', 'hash', FALSE, TRUE)", regularUserID)
	regularSess, _ := auth.CreateSession(ctx, db, regularUserID)

	// 2. Create an admin user
	adminUserID := uuid.New().String()
	_, _ = db.ExecContext(ctx, "INSERT INTO users (id, username, password_hash, is_platform_admin, is_active) VALUES ($1, 'admin_user', 'hash', TRUE, TRUE)", adminUserID)
	adminSess, _ := auth.CreateSession(ctx, db, adminUserID)

	userHandler := &admin.UserAdminHandler{
		DB:        db,
		Templates: &mockRenderer{},
	}

	adminRouteStack := auth.RequireAuth(db, false)(admin.RequireAdmin(http.HandlerFunc(userHandler.ListUsers)))

	// Case A: Non-admin request should get 403 Forbidden
	reqNonAdmin := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	reqNonAdmin.AddCookie(&http.Cookie{Name: auth.CookieSessionName, Value: regularSess.ID})
	recNonAdmin := httptest.NewRecorder()

	adminRouteStack.ServeHTTP(recNonAdmin, reqNonAdmin)
	if recNonAdmin.Code != http.StatusForbidden {
		t.Errorf("Expected HTTP 403 Forbidden for non-admin, got %d", recNonAdmin.Code)
	}

	// Case B: Admin request should get 200 OK
	reqAdmin := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	reqAdmin.AddCookie(&http.Cookie{Name: auth.CookieSessionName, Value: adminSess.ID})
	recAdmin := httptest.NewRecorder()

	adminRouteStack.ServeHTTP(recAdmin, reqAdmin)
	if recAdmin.Code != http.StatusOK {
		t.Errorf("Expected HTTP 200 OK for platform admin, got %d", recAdmin.Code)
	}
}

// Integration Test 2: TestPrivilegeEscalationRotatesSessionWithoutLogout
// Verifies that when privileges are increased (user promoted to admin or added to group),
// the session identifier is rotated in the backend, the old token remains valid during the 30s grace period,
// the user continues seamlessly without being logged out, and absolute expiration is preserved.
func TestPrivilegeEscalationRotatesSessionWithoutLogout(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()

	// Create user
	userID := uuid.New().String()
	_, _ = db.ExecContext(ctx, "INSERT INTO users (id, username, password_hash, is_platform_admin, is_active) VALUES ($1, 'escalate_user', 'hash', FALSE, TRUE)", userID)

	initialSess, err := auth.CreateSession(ctx, db, userID)
	if err != nil {
		t.Fatalf("CreateSession error: %v", err)
	}
	oldToken := initialSess.ID

	userHandler := &admin.UserAdminHandler{
		DB:        db,
		Templates: &mockRenderer{},
	}

	// Simulate Admin promoting the user to Platform Admin via POST /admin/users/{id}/toggle-admin
	toggleReq := httptest.NewRequest(http.MethodPost, "/admin/users/"+userID+"/toggle-admin", nil)
	toggleReq.SetPathValue("id", userID)
	toggleRec := httptest.NewRecorder()

	userHandler.ToggleAdmin(toggleRec, toggleReq)

	// Verify promotion in DB
	var isNowAdmin bool
	_ = db.QueryRowContext(ctx, "SELECT is_platform_admin FROM users WHERE id = $1", userID).Scan(&isNowAdmin)
	if !isNowAdmin {
		t.Fatalf("User was not promoted to platform admin")
	}

	// The session should now be rotated in the DB
	// Looking up with oldToken should succeed because of the 30-second grace period!
	sessGrace, wasPrevious, err := auth.GetSession(ctx, db, oldToken)
	if err != nil {
		t.Fatalf("Session with old token must succeed during grace period without logging user out: %v", err)
	}
	if !wasPrevious {
		t.Errorf("Expected wasPrevious=true for rotated session during grace period")
	}

	newToken := sessGrace.ID
	if newToken == oldToken {
		t.Fatalf("Session token must have rotated to a new token upon privilege escalation")
	}

	// Verify that absolute expiration was NOT extended
	if initialSess.ExpiresAt.Unix() != sessGrace.ExpiresAt.Unix() {
		t.Errorf("Absolute expiration must remain unchanged across rotation. Got %v, expected %v", sessGrace.ExpiresAt, initialSess.ExpiresAt)
	}

	// Verify that user makes a subsequent request with newToken successfully without logging out
	sessNew, wasPreviousNew, err := auth.GetSession(ctx, db, newToken)
	if err != nil || wasPreviousNew {
		t.Errorf("New rotated token must be directly valid: %v", err)
	}
	if sessNew.ID != newToken {
		t.Errorf("Expected session ID %s, got %s", newToken, sessNew.ID)
	}

	// Verify old token expires after grace period
	pastGrace := time.Now().UTC().Add(-35 * time.Second)
	_, _ = db.ExecContext(ctx, "UPDATE sessions SET grace_until = $1 WHERE id = $2", pastGrace, newToken)

	_, _, err = auth.GetSession(ctx, db, oldToken)
	if err == nil {
		t.Errorf("Old token must expire after 30-second grace period")
	}
}

// Integration Test 3: TestAccessRevocationDestroysSession
// Verifies that when access is revoked (e.g. user deactivated, removed from group, or grant revoked),
// the user's active backend sessions are immediately destroyed, forcing re-login.
func TestAccessRevocationDestroysSession(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()

	// 1. Create User
	userID := uuid.New().String()
	_, _ = db.ExecContext(ctx, "INSERT INTO users (id, username, password_hash, is_platform_admin, is_active) VALUES ($1, 'revoked_user', 'hash', FALSE, TRUE)", userID)

	sess, err := auth.CreateSession(ctx, db, userID)
	if err != nil {
		t.Fatalf("CreateSession error: %v", err)
	}

	userHandler := &admin.UserAdminHandler{
		DB:        db,
		Templates: &mockRenderer{},
	}

	// Simulate Admin deactivating the user
	deactivateReq := httptest.NewRequest(http.MethodPost, "/admin/users/"+userID+"/toggle-active", nil)
	deactivateReq.SetPathValue("id", userID)
	deactivateRec := httptest.NewRecorder()

	userHandler.ToggleActive(deactivateRec, deactivateReq)

	// Verify user is inactive in DB
	var isActive bool
	_ = db.QueryRowContext(ctx, "SELECT is_active FROM users WHERE id = $1", userID).Scan(&isActive)
	if isActive {
		t.Fatalf("User was not deactivated")
	}

	// Verify that session was destroyed in the backend
	var sessionCount int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sessions WHERE id = $1", sess.ID).Scan(&sessionCount)
	if sessionCount != 0 {
		t.Errorf("Session was not revoked in backend upon user deactivation")
	}

	// Next request with that session cookie must be rejected
	_, _, err = auth.GetSession(ctx, db, sess.ID)
	if err == nil {
		t.Errorf("Request with destroyed session must be rejected, requiring re-login")
	}

	// 2. Test group removal revocation
	// Create second user and add to a group
	user2ID := uuid.New().String()
	groupID := uuid.New().String()
	_, _ = db.ExecContext(ctx, "INSERT INTO users (id, username, password_hash, is_platform_admin, is_active) VALUES ($1, 'group_revoked_user', 'hash', FALSE, TRUE)", user2ID)
	_, _ = db.ExecContext(ctx, "INSERT INTO groups (id, name) VALUES ($1, 'SecurityTeam')", groupID)
	_, _ = db.ExecContext(ctx, "INSERT INTO user_groups (user_id, group_id) VALUES ($1, $2)", user2ID, groupID)

	sess2, _ := auth.CreateSession(ctx, db, user2ID)

	groupHandler := &admin.GroupAdminHandler{
		DB:        db,
		Templates: &mockRenderer{},
	}

	// Admin removes user2 from group
	removeForm := url.Values{}
	removeForm.Set("user_id", user2ID)
	removeReq := httptest.NewRequest(http.MethodPost, "/admin/groups/"+groupID+"/members/remove", strings.NewReader(removeForm.Encode()))
	removeReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	removeReq.SetPathValue("id", groupID)
	removeRec := httptest.NewRecorder()

	groupHandler.RemoveMember(removeRec, removeReq)

	// Verify that user2's sessions are revoked
	var session2Count int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sessions WHERE id = $1", sess2.ID).Scan(&session2Count)
	if session2Count != 0 {
		t.Errorf("Sessions must be destroyed when user is removed from group")
	}
}
