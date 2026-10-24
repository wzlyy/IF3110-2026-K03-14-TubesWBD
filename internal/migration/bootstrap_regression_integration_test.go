//go:build integration

package migration

import (
	"context"
	"testing"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/config"
)

func TestBootstrapRequiresExplicitSecret(t *testing.T) {
	f := migrationFixture(t, "postgres")
	t.Setenv("APP_ENV", "production")
	t.Setenv("ADMIN_INITIAL_USERNAME", "")
	t.Setenv("ADMIN_INITIAL_PASSWORD", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminInitialPassword != "" {
		t.Error("configuration invented a public bootstrap password")
	}
	if err := auth.BootstrapPlatformAdmin(context.Background(), f.db, cfg.AdminInitialUsername, cfg.AdminInitialPassword); err == nil {
		t.Fatal("bootstrap accepted missing secret")
	}
	var count int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM users WHERE is_platform_admin=true").Scan(&count); err != nil || count != 0 {
		t.Fatalf("admin created without secret: %d %v", count, err)
	}
	if err := auth.BootstrapPlatformAdmin(context.Background(), f.db, "admin", "Admin@Secure123!"); err == nil {
		t.Fatal("bootstrap accepted retired public password")
	}
	if err := auth.BootstrapPlatformAdmin(context.Background(), f.db, "admin", "ExplicitTestOnly!2026"); err != nil {
		t.Fatal(err)
	}
	if err := auth.BootstrapPlatformAdmin(context.Background(), f.db, "admin", ""); err != nil {
		t.Fatalf("existing installation must not require initial secret again: %v", err)
	}
}
