package auth_test

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
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/config"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
)

// Unit Test 1: TestPasswordHashing tests bcrypt hashing, valid password check, and invalid password rejection.
func TestPasswordHashing(t *testing.T) {
	password := "SecretPassword123!"

	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}

	if hash == password {
		t.Errorf("Password was not hashed, plaintext stored!")
	}

	// Verify valid password
	if err := auth.CheckPassword(hash, password); err != nil {
		t.Errorf("CheckPassword failed on valid password: %v", err)
	}

	// Verify invalid password rejection
	if err := auth.CheckPassword(hash, "WrongPassword"); err == nil {
		t.Errorf("CheckPassword should have failed on wrong password, but succeeded")
	}

	// Verify short password rejection
	if _, err := auth.HashPassword("12345"); err == nil {
		t.Errorf("HashPassword should have rejected password shorter than 6 chars")
	}
}

// Unit Test 2: TestSessionTimeouts tests idle timeout (30m) and absolute expiration (8h).
func TestSessionTimeouts(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()

	// Create user
	userID := uuid.New().String()
	_, err := db.ExecContext(ctx, "INSERT INTO users (id, username, password_hash) VALUES ($1, 'timeout_user', 'hash')", userID)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// A. Test Idle Timeout
	sess1, err := auth.CreateSession(ctx, db, userID)
	if err != nil {
		t.Fatalf("CreateSession error: %v", err)
	}

	// Simulate inactivity: set last_active_at to 31 minutes ago
	staleTime := time.Now().UTC().Add(-31 * time.Minute)
	_, err = db.ExecContext(ctx, "UPDATE sessions SET last_active_at = $1 WHERE id = $2", staleTime, sess1.ID)
	if err != nil {
		t.Fatalf("Failed to update session: %v", err)
	}

	_, _, err = auth.GetSession(ctx, db, sess1.ID)
	if err == nil {
		t.Errorf("GetSession should have failed with idle timeout after 31m of inactivity")
	}

	// B. Test Absolute Expiration (8h)
	sess2, err := auth.CreateSession(ctx, db, userID)
	if err != nil {
		t.Fatalf("CreateSession error: %v", err)
	}

	// Simulate session created 8 hours and 1 minute ago, even if recently active
	expiredTime := time.Now().UTC().Add(-8*time.Hour - 1*time.Minute)
	recentTime := time.Now().UTC()
	_, err = db.ExecContext(ctx, "UPDATE sessions SET created_at = $1, expires_at = $1, last_active_at = $2 WHERE id = $3", expiredTime, recentTime, sess2.ID)
	if err != nil {
		t.Fatalf("Failed to update session: %v", err)
	}

	_, _, err = auth.GetSession(ctx, db, sess2.ID)
	if err == nil {
		t.Errorf("GetSession should have failed because absolute expiration reached, despite recent activity")
	}
}

// Unit Test 3: TestSessionRotationAndGracePeriod tests token rotation and 30-second grace period.
func TestSessionRotationAndGracePeriod(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()

	userID := uuid.New().String()
	_, _ = db.ExecContext(ctx, "INSERT INTO users (id, username, password_hash) VALUES ($1, 'rotate_user', 'hash')", userID)

	initialSess, err := auth.CreateSession(ctx, db, userID)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	originalToken := initialSess.ID

	// Rotate session
	rotatedSess, err := auth.RotateSession(ctx, db, originalToken)
	if err != nil {
		t.Fatalf("RotateSession failed: %v", err)
	}

	newToken := rotatedSess.ID
	if newToken == originalToken {
		t.Errorf("Rotated token must be different from original token")
	}

	// 1. New token is immediately valid
	sessFromNew, wasPrev, err := auth.GetSession(ctx, db, newToken)
	if err != nil || wasPrev {
		t.Errorf("GetSession with new token failed: %v, wasPrev=%v", err, wasPrev)
	}
	if sessFromNew.ID != newToken {
		t.Errorf("Expected session ID %s, got %s", newToken, sessFromNew.ID)
	}

	// 2. Old token is valid within the 30-second grace period
	sessFromOld, wasPrev, err := auth.GetSession(ctx, db, originalToken)
	if err != nil {
		t.Errorf("GetSession with old token in grace period failed: %v", err)
	}
	if !wasPrev {
		t.Errorf("Expected wasPrevious=true for old token in grace period")
	}
	if sessFromOld.ID != newToken {
		t.Errorf("Session retrieved via grace period must reference current ID %s", newToken)
	}

	// 3. Old token expires after grace period has elapsed
	pastGrace := time.Now().UTC().Add(-35 * time.Second)
	_, _ = db.ExecContext(ctx, "UPDATE sessions SET grace_until = $1 WHERE id = $2", pastGrace, newToken)

	_, _, err = auth.GetSession(ctx, db, originalToken)
	if err == nil {
		t.Errorf("GetSession with old token must fail after grace period elapsed")
	}

	// 4. Absolute expiration did NOT change
	if initialSess.ExpiresAt.Unix() != rotatedSess.ExpiresAt.Unix() {
		t.Errorf("Absolute expiration must remain unchanged across rotation. Got %v, expected %v", rotatedSess.ExpiresAt, initialSess.ExpiresAt)
	}
}

// Integration Test 1: TestBootstrapPlatformAdminCreatesInitialAccount
func TestBootstrapPlatformAdminCreatesInitialAccount(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()

	// Ensure DB is empty
	_, _ = db.ExecContext(ctx, "DELETE FROM users")

	adminUser := "superadmin"
	adminPass := "SuperSecret123!"

	// First bootstrap: should create the account
	err := auth.BootstrapPlatformAdmin(ctx, db, adminUser, adminPass)
	if err != nil {
		t.Fatalf("First bootstrap failed: %v", err)
	}

	var count int
	var hash string
	var isAdmin, isActive bool
	err = db.QueryRowContext(ctx, "SELECT COUNT(*), password_hash, is_platform_admin, is_active FROM users WHERE username = $1 GROUP BY password_hash, is_platform_admin, is_active", adminUser).
		Scan(&count, &hash, &isAdmin, &isActive)
	if err != nil {
		t.Fatalf("Admin account query failed: %v", err)
	}

	if count != 1 || !isAdmin || !isActive {
		t.Errorf("Expected 1 active platform admin, got count=%d, admin=%v, active=%v", count, isAdmin, isActive)
	}

	if err := auth.CheckPassword(hash, adminPass); err != nil {
		t.Errorf("Password hash verification failed for bootstrapped admin: %v", err)
	}

	// Second bootstrap (idempotency test): should skip and NOT create duplicate
	err = auth.BootstrapPlatformAdmin(ctx, db, adminUser, "AnotherPassword")
	if err != nil {
		t.Fatalf("Second bootstrap failed: %v", err)
	}

	var totalAdmins int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE is_platform_admin = TRUE").Scan(&totalAdmins)
	if totalAdmins != 1 {
		t.Errorf("Idempotency violation: expected exactly 1 admin, found %d", totalAdmins)
	}
}

// Integration Test 2: TestLoginSuccessAndCookieFlags
func TestLoginSuccessAndCookieFlags(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	username := "login_test_user"
	password := "SecurePass123!"
	hash, _ := auth.HashPassword(password)
	_, _ = db.ExecContext(ctx, "INSERT INTO users (id, username, password_hash, is_active) VALUES ($1, $2, $3, TRUE)", uuid.New().String(), username, hash)

	handler := &auth.AuthHandler{
		DB:           db,
		Templates:    &mockRenderer{},
		SecureCookie: true,
	}

	form := url.Values{}
	form.Set("username", username)
	form.Set("password", password)

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	handler.ProcessLogin(rec, req)

	res := rec.Result()
	if res.StatusCode != http.StatusSeeOther {
		t.Errorf("Expected redirect 303, got %d", res.StatusCode)
	}

	cookies := res.Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == auth.CookieSessionName {
			sessionCookie = c
			break
		}
	}

	if sessionCookie == nil {
		t.Fatalf("Session cookie not set in response!")
	}

	if !sessionCookie.HttpOnly {
		t.Errorf("Expected HttpOnly=true on session cookie")
	}

	if sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("Expected SameSite=Lax, got %v", sessionCookie.SameSite)
	}

	if !sessionCookie.Secure {
		t.Errorf("Expected Secure=true when SecureCookie is configured")
	}

	// Secret leak prevention: ensure password and session token do not appear in response body
	body := rec.Body.String()
	if strings.Contains(body, password) {
		t.Errorf("Security leak: password appeared in response body!")
	}
	if strings.Contains(body, sessionCookie.Value) {
		t.Errorf("Security leak: session token appeared in response body!")
	}
}

// Integration Test 3: TestLoginFailureInvalidCredentials
func TestLoginFailureInvalidCredentials(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	handler := &auth.AuthHandler{
		DB:        db,
		Templates: &mockRenderer{},
	}

	form := url.Values{}
	form.Set("username", "non_existent_user")
	form.Set("password", "AnyPassword")

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	handler.ProcessLogin(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 Unauthorized for invalid login, got %d", rec.Code)
	}

	// No session cookie should be issued
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieSessionName && c.Value != "" {
			t.Errorf("Session cookie should not be set on failed login")
		}
	}
}

// Integration Test 4: TestCSRFProtection
func TestCSRFProtection(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	userID := uuid.New().String()
	_, _ = db.ExecContext(ctx, "INSERT INTO users (id, username, password_hash) VALUES ($1, 'csrf_user', 'hash')", userID)

	sess, err := auth.CreateSession(ctx, db, userID)
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	targetHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Action executed"))
	})

	stack := auth.RequireAuth(db, false)(auth.CSRFProtectionMiddleware(targetHandler))

	// Case A: Missing CSRF token -> Rejected 403
	reqNoToken := httptest.NewRequest(http.MethodPost, "/admin/action", strings.NewReader("val=1"))
	reqNoToken.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqNoToken.AddCookie(&http.Cookie{Name: auth.CookieSessionName, Value: sess.ID})
	recNoToken := httptest.NewRecorder()

	stack.ServeHTTP(recNoToken, reqNoToken)
	if recNoToken.Code != http.StatusForbidden {
		t.Errorf("Expected 403 Forbidden without CSRF token, got %d", recNoToken.Code)
	}

	// Case B: Wrong CSRF token -> Rejected 403
	formWrong := url.Values{}
	formWrong.Set("csrf_token", "invalid_token_12345678901234567890123456789012")
	reqWrong := httptest.NewRequest(http.MethodPost, "/admin/action", strings.NewReader(formWrong.Encode()))
	reqWrong.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqWrong.AddCookie(&http.Cookie{Name: auth.CookieSessionName, Value: sess.ID})
	recWrong := httptest.NewRecorder()

	stack.ServeHTTP(recWrong, reqWrong)
	if recWrong.Code != http.StatusForbidden {
		t.Errorf("Expected 403 Forbidden with wrong CSRF token, got %d", recWrong.Code)
	}

	// Case C: Valid CSRF token -> Allowed 200
	formValid := url.Values{}
	formValid.Set("csrf_token", sess.CSRFToken)
	reqValid := httptest.NewRequest(http.MethodPost, "/admin/action", strings.NewReader(formValid.Encode()))
	reqValid.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqValid.AddCookie(&http.Cookie{Name: auth.CookieSessionName, Value: sess.ID})
	recValid := httptest.NewRecorder()

	stack.ServeHTTP(recValid, reqValid)
	if recValid.Code != http.StatusOK {
		t.Errorf("Expected 200 OK with valid CSRF token, got %d", recValid.Code)
	}
}

// Integration Test 5: TestSessionRevocationOnLogout
func TestSessionRevocationOnLogout(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	userID := uuid.New().String()
	_, _ = db.ExecContext(ctx, "INSERT INTO users (id, username, password_hash) VALUES ($1, 'logout_user', 'hash')", userID)

	sess, _ := auth.CreateSession(ctx, db, userID)

	handler := &auth.AuthHandler{
		DB:        db,
		Templates: &mockRenderer{},
	}

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieSessionName, Value: sess.ID})
	rec := httptest.NewRecorder()

	handler.ProcessLogout(rec, req)

	// Check that session was deleted from database
	var count int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sessions WHERE id = $1", sess.ID).Scan(&count)
	if count != 0 {
		t.Errorf("Session was not revoked in database upon logout")
	}

	// Check cookie was cleared
	cookies := rec.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == auth.CookieSessionName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil || sessionCookie.MaxAge != -1 {
		t.Errorf("Session cookie must be cleared with MaxAge=-1 on logout")
	}
}

// mockRenderer for handler tests
type mockRenderer struct{}

func (m *mockRenderer) ExecuteTemplate(w io.Writer, name string, data any) error {
	return nil
}

// setupTestDB helper
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
