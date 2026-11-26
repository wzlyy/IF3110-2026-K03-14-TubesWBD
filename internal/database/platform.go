package database

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/config"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/migrations"
)

// NewPlatformDB initializes and verifies the PostgreSQL connection pool for the platform.
func NewPlatformDB(cfg *config.Config) (*sql.DB, error) {
	dsn := cfg.PlatformDB.DSN()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database connection: %w", err)
	}

	// Configure pool parameters
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(15 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to ping platform database at %s:%d: %w", cfg.PlatformDB.Host, cfg.PlatformDB.Port, err)
	}

	return db, nil
}

// MigratePlatformDB applies the embedded initial schema and additive upgrades idempotently.
func MigratePlatformDB(ctx context.Context, db *sql.DB) error {
	files, err := fs.Glob(migrations.PlatformFS, "platform/*.sql")
	if err != nil {
		return fmt.Errorf("failed to list embedded platform schemas: %w", err)
	}
	sort.Strings(files)
	if len(files) == 0 {
		return fmt.Errorf("no embedded platform schemas found")
	}

	for _, name := range files {
		content, err := fs.ReadFile(migrations.PlatformFS, name)
		if err != nil {
			return fmt.Errorf("failed to read platform schema %s: %w", name, err)
		}
		if _, err := db.ExecContext(ctx, string(content)); err != nil {
			return fmt.Errorf("failed to apply platform schema %s: %w", name, err)
		}
	}

	return nil
}
