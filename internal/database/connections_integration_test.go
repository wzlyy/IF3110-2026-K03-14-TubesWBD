//go:build integration

package database

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/config"
)

func connectionFixture(t *testing.T) (*ConnectionStore, *TargetManager) {
	t.Helper()
	if os.Getenv("WBD_TEST_DATABASES") != "1" {
		t.Skip("use scripts/test-integration.ps1 with dedicated test databases")
	}
	if os.Getenv("PLATFORM_DB_HOST") == "" || os.Getenv("TARGET_PG_HOST") == "" {
		t.Skip("dedicated PostgreSQL test environment not configured")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	root, err := NewPlatformDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	schema := "connections_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := root.ExecContext(context.Background(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := root.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		if err != nil {
			t.Error(err)
		}
	})
	pgcfg, err := pgx.ParseConfig(cfg.PlatformDB.DSN())
	if err != nil {
		t.Fatal(err)
	}
	pgcfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*pgcfg)
	t.Cleanup(func() { _ = db.Close() })
	if err := MigratePlatformDB(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	store, err := NewConnectionStore(db, cfg.EncryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewTargetManager(store)
	t.Cleanup(func() { _ = manager.Close() })
	return store, manager
}

func targetFixture() (Connection, string) {
	port, _ := strconv.Atoi(os.Getenv("TARGET_PG_PORT"))
	return Connection{Name: "Integration PostgreSQL", DBMS: "postgres", Host: os.Getenv("TARGET_PG_HOST"), Port: port, DBName: os.Getenv("TARGET_PG_NAME"), Username: os.Getenv("TARGET_PG_USER")}, os.Getenv("TARGET_PG_PASSWORD")
}

func TestConnectionStoreAndTargetLifecycle(t *testing.T) {
	store, manager := connectionFixture(t)
	ctx := context.Background()
	c, password := targetFixture()
	saved, err := store.Create(ctx, c, password)
	if err != nil {
		t.Fatal(err)
	}
	var encrypted string
	if err := store.db.QueryRowContext(ctx, "SELECT password_encrypted FROM database_connections WHERE id=$1", saved.ID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if encrypted == password || strings.Contains(encrypted, password) {
		t.Fatal("plaintext credential was stored")
	}
	plain, err := store.cipher.decrypt(encrypted)
	if err != nil || plain != password {
		t.Fatalf("stored ciphertext roundtrip: %v", err)
	}
	if err := manager.Test(ctx, saved.ID); err != nil {
		t.Fatal(err)
	}
	pool, dbms, err := manager.Open(ctx, saved.ID)
	if err != nil || dbms != "postgres" || pool.Stats().MaxOpenConnections != 5 {
		t.Fatalf("invalid target pool: %s %v", dbms, err)
	}
	c.Name = "Edited PostgreSQL"
	if err := store.Update(ctx, saved.ID, c, ""); err != nil {
		t.Fatal(err)
	}
	manager.Invalidate(saved.ID)
	var preserved string
	if err := store.db.QueryRowContext(ctx, "SELECT password_encrypted FROM database_connections WHERE id=$1", saved.ID).Scan(&preserved); err != nil {
		t.Fatal(err)
	}
	if preserved != encrypted {
		t.Fatal("blank edit did not preserve ciphertext")
	}
	if err := pool.PingContext(ctx); !errors.Is(err, sql.ErrConnDone) && err == nil {
		t.Fatal("old pool remains usable")
	}
	replacement, _, err := manager.Open(ctx, saved.ID)
	if err != nil || replacement == pool {
		t.Fatalf("pool not replaced: %v", err)
	}
	if err := manager.Test(ctx, saved.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, saved.ID, c, "wrong-test-password"); err != nil {
		t.Fatal(err)
	}
	manager.Invalidate(saved.ID)
	started := time.Now()
	if err := manager.Test(ctx, saved.ID); err == nil || strings.Contains(err.Error(), "wrong-test-password") || strings.Contains(err.Error(), "SQLSTATE") {
		t.Fatalf("expected sanitized ping failure: %v", err)
	}
	if time.Since(started) > 6*time.Second {
		t.Fatal("test exceeded deadline")
	}
	if err := store.Update(ctx, saved.ID, c, password); err != nil {
		t.Fatal(err)
	}
	manager.Invalidate(saved.ID)
	if err := manager.Test(ctx, saved.ID); err != nil {
		t.Fatal(err)
	}
	// Save unreachable metadata without trying to establish a network connection.
	unreachable := c
	unreachable.Host = "unreachable.invalid"
	if _, err := store.Create(ctx, unreachable, password); err != nil {
		t.Fatalf("saving unreachable settings failed: %v", err)
	}
	if err := store.Delete(ctx, saved.ID); err != nil {
		t.Fatal(err)
	}
	manager.Invalidate(saved.ID)
	if _, err := store.Get(ctx, saved.ID); !errors.Is(err, ErrConnectionNotFound) {
		t.Fatalf("deleted metadata: %v", err)
	}
	if _, _, err := manager.Open(ctx, saved.ID); !errors.Is(err, ErrConnectionNotFound) {
		t.Fatalf("deleted target reopened: %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.Open(ctx, "missing"); err == nil {
		t.Fatal("closed manager reopened")
	}
}

func TestTargetInvalidationRaces(t *testing.T) {
	store, manager := connectionFixture(t)
	c, password := targetFixture()
	saved, err := store.Create(context.Background(), c, password)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				_, _, _ = manager.Open(context.Background(), saved.ID)
				manager.Invalidate(saved.ID)
			}
		}()
	}
	if err := store.Delete(context.Background(), saved.ID); err != nil {
		t.Fatal(err)
	}
	manager.Invalidate(saved.ID)
	wg.Wait()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.pools[saved.ID] != nil {
		t.Fatal("stale pool republished after delete")
	}
}

func TestTargetTestHonorsCallerDeadline(t *testing.T) {
	store, manager := connectionFixture(t)
	// A listening socket that never answers the PostgreSQL handshake proves that
	// the deadline also bounds an unresponsive target, not just refused TCP.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	c, password := targetFixture()
	c.Host = "127.0.0.1"
	c.Port = listener.Addr().(*net.TCPAddr).Port
	saved, err := store.Create(context.Background(), c, password)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = manager.Test(ctx, saved.ID)
	if err == nil || strings.Contains(err.Error(), password) {
		t.Fatalf("expected safe timeout error, got %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("target test ignored caller deadline")
	}
}

func TestTargetMetadataContentionHonorsCallerDeadline(t *testing.T) {
	for _, sameID := range []bool{true, false} {
		t.Run(strconv.FormatBool(sameID), func(t *testing.T) {
			store, manager := connectionFixture(t)
			c, password := targetFixture()
			first, err := store.Create(context.Background(), c, password)
			if err != nil {
				t.Fatal(err)
			}
			second, err := store.Create(context.Background(), c, password)
			if err != nil {
				t.Fatal(err)
			}
			if sameID {
				second = first
			}
			tx, err := store.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.Exec("LOCK TABLE database_connections IN ACCESS EXCLUSIVE MODE"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			finished := make(chan error, 1)
			go func() { _, _, err := manager.Open(ctx, first.ID); finished <- err }()
			// Observe the real blocked metadata SELECT before starting the short caller.
			deadline := time.Now().Add(2 * time.Second)
			for {
				var blocked bool
				if _, err := tx.Exec("SELECT pg_stat_clear_snapshot()"); err != nil {
					t.Fatal(err)
				}
				err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_stat_activity
					WHERE datname=current_database() AND wait_event_type='Lock'
					AND query LIKE 'SELECT id, name, dbms_type%FROM database_connections WHERE id=$1%')`).Scan(&blocked)
				if err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("metadata SELECT did not block")
				}
				time.Sleep(10 * time.Millisecond)
			}
			short, stop := context.WithTimeout(context.Background(), 150*time.Millisecond)
			started := time.Now()
			err = manager.Test(short, second.ID)
			elapsed := time.Since(started)
			stop()
			cancel()
			_ = tx.Rollback()
			<-finished
			if err == nil {
				t.Fatal("expected canceled metadata test")
			}
			if elapsed > time.Second {
				t.Fatalf("metadata contention ignored caller deadline: %v", elapsed)
			}
		})
	}
}
