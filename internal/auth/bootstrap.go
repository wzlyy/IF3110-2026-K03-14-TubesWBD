package auth

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	"github.com/google/uuid"
)

// BootstrapPlatformAdmin ensures an initial platform admin account exists in the platform database.
// This function is strictly idempotent: if an active platform admin already exists, it does nothing.
func BootstrapPlatformAdmin(ctx context.Context, db *sql.DB, username, password string) error {
	if username == "" {
		username = "admin"
	}
	if password == "" {
		password = "Admin@Secure123!"
	}

	var count int
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE is_platform_admin = TRUE").Scan(&count)
	if err != nil {
		return fmt.Errorf("failed to check existing platform admins: %w", err)
	}

	if count > 0 {
		log.Println("[BOOTSTRAP] Platform admin account already present. Skipping initial bootstrap.")
		return nil
	}

	hash, err := HashPassword(password)
	if err != nil {
		return fmt.Errorf("failed to hash initial admin password: %w", err)
	}

	adminID := uuid.New().String()
	query := `
		INSERT INTO users (id, username, password_hash, is_platform_admin, is_active, created_at)
		VALUES ($1, $2, $3, TRUE, TRUE, CURRENT_TIMESTAMP)
		ON CONFLICT (username) DO NOTHING
	`

	_, err = db.ExecContext(ctx, query, adminID, username, hash)
	if err != nil {
		return fmt.Errorf("failed to insert initial platform admin: %w", err)
	}

	log.Printf("[BOOTSTRAP] Successfully initialized default platform admin '%s'.", username)
	return nil
}
