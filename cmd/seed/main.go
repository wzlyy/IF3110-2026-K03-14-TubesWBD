package main

import (
	"context"
	"database/sql"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/config"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("[SEED] config load failed: %v", err)
	}

	db, err := database.NewPlatformDB(cfg)
	if err != nil {
		log.Fatalf("[SEED] platform db connect failed: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := database.MigratePlatformDB(ctx, db); err != nil {
		log.Fatalf("[SEED] platform migrate failed: %v", err)
	}
	if err := auth.BootstrapPlatformAdmin(ctx, db, cfg.AdminInitialUsername, cfg.AdminInitialPassword); err != nil {
		log.Fatalf("[SEED] admin bootstrap failed: %v", err)
	}

	seedPass := envOr("SEED_USER_PASSWORD", "DevPass!2026")
	passHash, err := auth.HashPassword(seedPass)
	if err != nil {
		log.Fatalf("[SEED] hash password failed: %v", err)
	}

	users := []struct {
		id       string
		username string
	}{
		{"11111111-1111-1111-1111-111111111101", "leader_core"},
		{"11111111-1111-1111-1111-111111111102", "leader_analytics"},
		{"11111111-1111-1111-1111-111111111103", "editor_core"},
		{"11111111-1111-1111-1111-111111111104", "viewer_analytics"},
	}
	userIDs := make(map[string]string, len(users))
	for _, u := range users {
		var actualID string
		err := db.QueryRowContext(ctx, `
			INSERT INTO users (id, username, password_hash, is_platform_admin, is_active)
			VALUES ($1, $2, $3, FALSE, TRUE)
			ON CONFLICT (username) DO UPDATE SET is_active = TRUE
			RETURNING id
		`, u.id, u.username, passHash).Scan(&actualID)
		if err != nil {
			log.Fatalf("[SEED] upsert user %s failed: %v", u.username, err)
		}
		userIDs[u.username] = actualID
	}

	groups := []struct {
		id       string
		name     string
		leader   string
		members  []string
	}{
		{"22222222-2222-2222-2222-222222222201", "team-core", "leader_core", []string{"leader_core", "editor_core"}},
		{"22222222-2222-2222-2222-222222222202", "team-analytics", "leader_analytics", []string{"leader_analytics", "viewer_analytics"}},
	}
	groupIDs := make(map[string]string, len(groups))
	for _, g := range groups {
		var actualID string
		err := db.QueryRowContext(ctx, `
			INSERT INTO groups (id, name, leader_user_id)
			VALUES ($1, $2, $3)
			ON CONFLICT (name) DO UPDATE SET leader_user_id = EXCLUDED.leader_user_id
			RETURNING id
		`, g.id, g.name, userIDs[g.leader]).Scan(&actualID)
		if err != nil {
			log.Fatalf("[SEED] upsert group %s failed: %v", g.name, err)
		}
		groupIDs[g.name] = actualID
		for _, m := range g.members {
			if _, err := db.ExecContext(ctx, `INSERT INTO user_groups (user_id, group_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, userIDs[m], actualID); err != nil {
				log.Fatalf("[SEED] add member %s to %s failed: %v", m, g.name, err)
			}
		}
	}

	store, err := database.NewConnectionStore(db, cfg.EncryptionKey)
	if err != nil {
		log.Fatalf("[SEED] connection store init failed: %v", err)
	}

	pg1Port, _ := strconv.Atoi(envOr("TARGET_PG_PORT", "5433"))
	pg2Port, _ := strconv.Atoi(envOr("TARGET_PG2_PORT", "5434"))

	db1ID := ensureConnection(ctx, db, store, database.Connection{
		Name:     "target-postgres-1",
		DBMS:     "postgres",
		Host:     envOr("TARGET_PG_HOST", "127.0.0.1"),
		Port:     pg1Port,
		DBName:   envOr("TARGET_PG_NAME", "target_db"),
		Username: envOr("TARGET_PG_USER", "postgres"),
	}, envOr("TARGET_PG_PASSWORD", "postgres"))

	db2ID := ensureConnection(ctx, db, store, database.Connection{
		Name:     "target-postgres-2",
		DBMS:     "postgres",
		Host:     envOr("TARGET_PG2_HOST", "127.0.0.1"),
		Port:     pg2Port,
		DBName:   envOr("TARGET_PG2_NAME", "target_db_2"),
		Username: envOr("TARGET_PG2_USER", "postgres"),
	}, envOr("TARGET_PG2_PASSWORD", "postgres"))

	grants := []struct {
		group string
		dbID  string
		level string
	}{
		{"team-core", db1ID, "VIEW_EDIT"},
		{"team-analytics", db1ID, "VIEW_ONLY"},
		{"team-core", db2ID, "VIEW_EDIT"},
	}
	for _, gr := range grants {
		_, err := db.ExecContext(ctx, `
			INSERT INTO group_database_grants (group_id, database_id, access_level)
			VALUES ($1, $2, $3)
			ON CONFLICT (group_id, database_id) DO UPDATE SET access_level = EXCLUDED.access_level
		`, groupIDs[gr.group], gr.dbID, gr.level)
		if err != nil {
			log.Fatalf("[SEED] assign grant failed: %v", err)
		}
	}

	log.Println("[SEED] Milestone 2 demo users, groups with leaders, target PostgreSQL connections, and grants seeded successfully.")
}

func ensureConnection(ctx context.Context, db *sql.DB, store *database.ConnectionStore, conn database.Connection, password string) string {
	var existingID string
	err := db.QueryRowContext(ctx, "SELECT id FROM database_connections WHERE name = $1 LIMIT 1", conn.Name).Scan(&existingID)
	if err == nil && existingID != "" {
		return existingID
	}
	created, err := store.Create(ctx, conn, password)
	if err != nil {
		log.Fatalf("[SEED] create connection %s failed: %v", conn.Name, err)
	}
	return created.ID
}

func envOr(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
