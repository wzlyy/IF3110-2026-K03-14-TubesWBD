package main

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/access"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/admin"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/config"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/migration"
)

// TemplateMap holds separately parsed templates to prevent block collision.
type TemplateMap struct {
	templates map[string]*template.Template
}

func (tm *TemplateMap) ExecuteTemplate(w io.Writer, name string, data any) error {
	t, ok := tm.templates[name]
	if !ok {
		return fmt.Errorf("template %s not found in registry", name)
	}
	if name == "login.html" {
		return t.Execute(w, data)
	}
	return t.ExecuteTemplate(w, "layout.html", data)
}

// findWebDir locates the 'web' directory from the current or parent directories.
func findWebDir() (string, error) {
	candidates := []string{
		"web",
		"../../web",
		"../web",
	}
	for _, dir := range candidates {
		if stat, err := os.Stat(dir); err == nil && stat.IsDir() {
			return filepath.Abs(dir)
		}
	}
	return "", errors.New("could not find 'web' directory")
}

// loadTemplates builds the TemplateMap from disk.
func loadTemplates(webDir string) (*TemplateMap, error) {
	layoutPath := filepath.Join(webDir, "templates", "layout.html")
	if _, err := os.Stat(layoutPath); err != nil {
		return nil, fmt.Errorf("missing layout.html: %w", err)
	}

	tm := &TemplateMap{
		templates: make(map[string]*template.Template),
	}

	// 1. Standalone login template
	loginPath := filepath.Join(webDir, "templates", "auth", "login.html")
	loginTmpl, err := template.ParseFiles(loginPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse login.html: %w", err)
	}
	tm.templates["login.html"] = loginTmpl

	// 2. Admin users template (cloned with layout)
	usersPath := filepath.Join(webDir, "templates", "admin", "users.html")
	usersTmpl, err := template.ParseFiles(layoutPath, usersPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse users.html: %w", err)
	}
	tm.templates["users.html"] = usersTmpl

	// 3. Admin groups template (cloned with layout)
	groupsPath := filepath.Join(webDir, "templates", "admin", "groups.html")
	groupsTmpl, err := template.ParseFiles(layoutPath, groupsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse groups.html: %w", err)
	}
	tm.templates["groups.html"] = groupsTmpl

	// 4. Admin grants template (cloned with layout)
	grantsPath := filepath.Join(webDir, "templates", "admin", "grants.html")
	grantsTmpl, err := template.ParseFiles(layoutPath, grantsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse grants.html: %w", err)
	}
	tm.templates["grants.html"] = grantsTmpl
	connectionsTmpl, err := template.ParseFiles(layoutPath, filepath.Join(webDir, "templates", "admin", "connections.html"))
	if err != nil {
		return nil, fmt.Errorf("failed to parse connections.html: %w", err)
	}
	tm.templates["connections.html"] = connectionsTmpl

	for _, page := range []string{"list", "schema", "data", "explain"} {
		t, err := template.ParseFiles(layoutPath, filepath.Join(webDir, "templates", "database", page+".html"))
		if err != nil {
			return nil, fmt.Errorf("failed to parse database %s: %w", page, err)
		}
		tm.templates["database-"+page+".html"] = t
	}
	for _, page := range []string{"editor", "history", "detail", "diff"} {
		t, err := template.ParseFiles(layoutPath, filepath.Join(webDir, "templates", "migration", page+".html"))
		if err != nil {
			return nil, fmt.Errorf("failed to parse migration %s: %w", page, err)
		}
		tm.templates["migration-"+page+".html"] = t
	}

	return tm, nil
}

func main() {
	log.Println("[PLATFORM] Starting Database Management Platform...")

	// 1. Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("[FATAL] Configuration load failed: %v", err)
	}

	// 2. Connect to platform database
	db, err := database.NewPlatformDB(cfg)
	if err != nil {
		log.Fatalf("[FATAL] Platform database connection failed: %v", err)
	}
	defer db.Close()
	log.Printf("[PLATFORM] Connected to platform database at %s:%d/%s", cfg.PlatformDB.Host, cfg.PlatformDB.Port, cfg.PlatformDB.DBName)

	// 3. Apply platform schema migrations
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := database.MigratePlatformDB(ctx, db); err != nil {
		log.Fatalf("[FATAL] Database migration failed: %v", err)
	}
	log.Println("[PLATFORM] Database schema verified & migrated.")

	// 4. Seed initial Platform Admin account if empty
	if err := auth.BootstrapPlatformAdmin(ctx, db, cfg.AdminInitialUsername, cfg.AdminInitialPassword); err != nil {
		log.Fatalf("[FATAL] Platform admin bootstrap failed: %v", err)
	}

	// 5. Load UI templates and static assets
	webDir, err := findWebDir()
	if err != nil {
		log.Fatalf("[FATAL] Could not locate web directory: %v", err)
	}
	templates, err := loadTemplates(webDir)
	if err != nil {
		log.Fatalf("[FATAL] Failed to parse HTML templates: %v", err)
	}

	// 6. Initialize HTTP Handlers
	authHandler := &auth.AuthHandler{
		DB:           db,
		Templates:    templates,
		SecureCookie: cfg.CookieSecure,
	}

	userAdminHandler := &admin.UserAdminHandler{
		DB:        db,
		Templates: templates,
	}

	groupAdminHandler := &admin.GroupAdminHandler{
		DB:        db,
		Templates: templates,
	}

	grantAdminHandler := &admin.GrantAdminHandler{
		DB:        db,
		Templates: templates,
	}
	connectionStore, err := database.NewConnectionStore(db, cfg.EncryptionKey)
	if err != nil {
		log.Fatalf("[FATAL] Credential encryption initialization failed: %v", err)
	}
	targetManager := database.NewTargetManager(connectionStore)
	defer targetManager.Close()
	connectionAdminHandler := &database.ConnectionAdminHandler{
		Store: connectionStore, Manager: targetManager, Templates: templates,
	}
	viewerHandler := &database.ViewerHandler{Store: connectionStore, Manager: targetManager, Templates: templates}
	explainHandler := &database.ExplainHandler{Store: connectionStore, Manager: targetManager, Templates: templates}
	// Long-lived row locks have a separate pool, preserving audit/auth capacity.
	migrationLocks, err := database.NewPlatformDB(cfg)
	if err != nil {
		log.Fatal("[FATAL] Migration lock database unavailable")
	}
	defer migrationLocks.Close()
	migrationHandler := &migration.Handler{DB: db, Store: connectionStore, Runner: &migration.Runner{DB: db, Locks: migrationLocks, Manager: targetManager}, Templates: templates}

	// 7. Middlewares
	requireAuth := auth.RequireAuth(db, cfg.CookieSecure)
	csrfProtection := auth.CSRFProtectionMiddleware

	// Helper to chain middlewares
	protectAdmin := func(handler http.HandlerFunc) http.Handler {
		return requireAuth(csrfProtection(admin.RequireAdmin(handler)))
	}

	protectUser := func(handler http.HandlerFunc) http.Handler {
		return requireAuth(csrfProtection(handler))
	}

	// 8. Register Routes
	mux := http.NewServeMux()

	// Static Assets
	staticDir := filepath.Join(webDir, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir(staticDir))))

	// Root redirect
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})

	// Authentication Endpoints
	authHandler.RegisterRoutes(mux, requireAuth)

	// Platform Admin: Users
	mux.Handle("GET /admin/users", protectAdmin(userAdminHandler.ListUsers))
	mux.Handle("POST /admin/users", protectAdmin(userAdminHandler.CreateUser))
	mux.Handle("POST /admin/users/{id}/toggle-active", protectAdmin(userAdminHandler.ToggleActive))
	mux.Handle("POST /admin/users/{id}/toggle-admin", protectAdmin(userAdminHandler.ToggleAdmin))
	mux.Handle("POST /admin/users/{id}/reset-password", protectAdmin(userAdminHandler.ResetPassword))

	// Platform Admin: Groups & Membership
	mux.Handle("GET /admin/groups", protectAdmin(groupAdminHandler.ListGroups))
	mux.Handle("POST /admin/groups", protectAdmin(groupAdminHandler.CreateGroup))
	mux.Handle("POST /admin/groups/{id}/edit", protectAdmin(groupAdminHandler.EditGroup))
	mux.Handle("POST /admin/groups/{id}/delete", protectAdmin(groupAdminHandler.DeleteGroup))
	mux.Handle("POST /admin/groups/{id}/members/add", protectAdmin(groupAdminHandler.AddMember))
	mux.Handle("POST /admin/groups/{id}/members/remove", protectAdmin(groupAdminHandler.RemoveMember))

	// Platform Admin: Database Grants
	mux.Handle("GET /admin/grants", protectAdmin(grantAdminHandler.ListGrants))
	mux.Handle("POST /admin/grants", protectAdmin(grantAdminHandler.AssignGrant))
	mux.Handle("POST /admin/grants/delete", protectAdmin(grantAdminHandler.RevokeGrant))

	// Platform Admin: Target Connections
	mux.Handle("GET /admin/connections", protectAdmin(connectionAdminHandler.ListConnections))
	mux.Handle("POST /admin/connections", protectAdmin(connectionAdminHandler.CreateConnection))
	mux.Handle("GET /admin/connections/{id}/edit", protectAdmin(connectionAdminHandler.ShowEdit))
	mux.Handle("POST /admin/connections/{id}/edit", protectAdmin(connectionAdminHandler.EditConnection))
	mux.Handle("POST /admin/connections/{id}/delete", protectAdmin(connectionAdminHandler.DeleteConnection))
	mux.Handle("POST /admin/connections/{id}/test", protectAdmin(connectionAdminHandler.TestConnection))

	mux.Handle("GET /databases", protectUser(viewerHandler.ListDatabases))
	viewAccess := access.RequireDatabaseAccess(db, access.AccessViewOnly)
	mux.Handle("GET /databases/{database_id}/schema", requireAuth(viewAccess(http.HandlerFunc(viewerHandler.ShowSchema))))
	mux.Handle("GET /databases/{database_id}/data", requireAuth(viewAccess(http.HandlerFunc(viewerHandler.ShowData))))
	mux.Handle("GET /databases/{database_id}/explain", requireAuth(viewAccess(http.HandlerFunc(explainHandler.Show))))
	migrationHandler.RegisterRoutes(mux, requireAuth)
	mux.Handle("POST /databases/{database_id}/explain", requireAuth(database.LimitExplainBody(csrfProtection(viewAccess(http.HandlerFunc(explainHandler.Analyze))))))

	// 9. Start HTTP Server with Graceful Shutdown
	addr := ":" + cfg.Port
	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("[PLATFORM] HTTP Server listening at http://localhost:%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[FATAL] Server listen error: %v", err)
		}
	}()

	// Wait for interrupt signal for graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Println("[PLATFORM] Shutting down gracefully...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("[ERROR] Server shutdown error: %v", err)
	}
	log.Println("[PLATFORM] Server stopped.")
}
