package main

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/access"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/admin"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/api"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/config"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/lock"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/migration"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/queue"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/telemetry"
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
	telemetry.InitLogger(os.Stdout)

	// 1. Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("[FATAL] Configuration load failed: %v", err)
	}

	// 1b. Inisialisasi TracerProvider OpenTelemetry (stdout bawaan atau OTLP Collector)
	_, shutdownTracer, err := telemetry.InitTracer(context.Background(), cfg.OTelServiceName, cfg.OTelOTLPEndpoint, os.Stdout)
	if err != nil {
		log.Fatalf("[FATAL] Telemetry tracer initialization failed: %v", err)
	}
	defer func() {
		sCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracer(sCtx)
	}()

	telemetry.LogAudit(context.Background(), "server.startup", "starting",
		slog.String("service", cfg.OTelServiceName),
		slog.String("port", cfg.Port),
	)

	// 2. Connect to platform database
	db, err := database.NewPlatformDB(cfg)
	if err != nil {
		log.Fatalf("[FATAL] Platform database connection failed: %v", err)
	}
	defer db.Close()

	// 3. Jalankan migrasi skema platform (M1 + skema aditif M2)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := database.MigratePlatformDB(ctx, db); err != nil {
		log.Fatalf("[FATAL] Database migration failed: %v", err)
	}

	// 4. Seed initial Platform Admin account if empty
	if err := auth.BootstrapPlatformAdmin(ctx, db, cfg.AdminInitialUsername, cfg.AdminInitialPassword); err != nil {
		log.Fatalf("[FATAL] Platform admin bootstrap failed: %v", err)
	}

	// 5. Inisialisasi koneksi target, distributed lock, publisher RabbitMQ, dan metrik pool Prometheus
	connectionStore, err := database.NewConnectionStore(db, cfg.EncryptionKey)
	if err != nil {
		log.Fatalf("[FATAL] Credential encryption initialization failed: %v", err)
	}
	targetManager := database.NewTargetManager(connectionStore)
	defer targetManager.Close()

	metrics := telemetry.GlobalMetrics()
	metrics.RegisterDBPool("platform", db.Stats)
	metrics.RegisterDBPool("target_postgres", targetManager.AggregatePoolStats)
	metrics.RegisterJobInflightDB(db)

	lockManager := lock.NewManager(db)

	var publisher queue.Publisher
	rmqClient, rmqErr := queue.NewRabbitMQClient(cfg.RabbitMQURL)
	if rmqErr != nil {
		telemetry.LogAudit(context.Background(), "queue.connect", "error", slog.String("error", rmqErr.Error()))
	} else {
		publisher = rmqClient
		defer rmqClient.Close()
		telemetry.LogAudit(context.Background(), "queue.connect", "ok")
	}

	// 6. Daftarkan REST JSON API (/api/v1/*) dan Prometheus /metrics
	mux := http.NewServeMux()
	api.RegisterRoutes(mux, api.Dependencies{
		DB:           db,
		Store:        connectionStore,
		Manager:      targetManager,
		Locks:        lockManager,
		Publisher:    publisher,
		SecureCookie: cfg.CookieSecure,
		Metrics:      metrics,
	})

	// 7. Sajikan bundle SPA (frontend/dist) jika ada; jika belum, pertahankan tampilan lama M1.
	if api.HasSPABundle(cfg.SPADistDir) {
		mux.Handle("/", api.NewSPAHandler(cfg.SPADistDir))
	} else if webDir, err := findWebDir(); err == nil {
		if templates, terr := loadTemplates(webDir); terr == nil {
			authHandler := &auth.AuthHandler{DB: db, Templates: templates, SecureCookie: cfg.CookieSecure}
			userAdminHandler := &admin.UserAdminHandler{DB: db, Templates: templates}
			groupAdminHandler := &admin.GroupAdminHandler{DB: db, Templates: templates}
			grantAdminHandler := &admin.GrantAdminHandler{DB: db, Templates: templates}
			connectionAdminHandler := &database.ConnectionAdminHandler{Store: connectionStore, Manager: targetManager, Templates: templates}
			viewerHandler := &database.ViewerHandler{Store: connectionStore, Manager: targetManager, Templates: templates}
			explainHandler := &database.ExplainHandler{Store: connectionStore, Manager: targetManager, Templates: templates}

			migrationLocks, lerr := database.NewPlatformDB(cfg)
			if lerr != nil {
				log.Fatal("[FATAL] Migration lock database unavailable")
			}
			defer migrationLocks.Close()
			migrationHandler := &migration.Handler{
				DB:        db,
				Store:     connectionStore,
				Runner:    &migration.Runner{DB: db, Locks: migrationLocks, Manager: targetManager},
				Templates: templates,
			}

			requireAuth := auth.RequireAuth(db, cfg.CookieSecure)
			csrfProtection := auth.CSRFProtectionMiddleware
			protectAdmin := func(handler http.HandlerFunc) http.Handler {
				return requireAuth(csrfProtection(admin.RequireAdmin(handler)))
			}
			protectUser := func(handler http.HandlerFunc) http.Handler {
				return requireAuth(csrfProtection(handler))
			}

			staticDir := filepath.Join(webDir, "static")
			mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir(staticDir))))
			mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
			})
			authHandler.RegisterRoutes(mux, requireAuth)
			mux.Handle("GET /admin/users", protectAdmin(userAdminHandler.ListUsers))
			mux.Handle("POST /admin/users", protectAdmin(userAdminHandler.CreateUser))
			mux.Handle("POST /admin/users/{id}/toggle-active", protectAdmin(userAdminHandler.ToggleActive))
			mux.Handle("POST /admin/users/{id}/toggle-admin", protectAdmin(userAdminHandler.ToggleAdmin))
			mux.Handle("POST /admin/users/{id}/reset-password", protectAdmin(userAdminHandler.ResetPassword))
			mux.Handle("GET /admin/groups", protectAdmin(groupAdminHandler.ListGroups))
			mux.Handle("POST /admin/groups", protectAdmin(groupAdminHandler.CreateGroup))
			mux.Handle("POST /admin/groups/{id}/edit", protectAdmin(groupAdminHandler.EditGroup))
			mux.Handle("POST /admin/groups/{id}/delete", protectAdmin(groupAdminHandler.DeleteGroup))
			mux.Handle("POST /admin/groups/{id}/members/add", protectAdmin(groupAdminHandler.AddMember))
			mux.Handle("POST /admin/groups/{id}/members/remove", protectAdmin(groupAdminHandler.RemoveMember))
			mux.Handle("GET /admin/grants", protectAdmin(grantAdminHandler.ListGrants))
			mux.Handle("POST /admin/grants", protectAdmin(grantAdminHandler.AssignGrant))
			mux.Handle("POST /admin/grants/delete", protectAdmin(grantAdminHandler.RevokeGrant))
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
		}
	}

	// 8. Jalankan server HTTP dengan middleware OTel dan shutdown teratur
	addr := ":" + cfg.Port
	srv := &http.Server{
		Addr:              addr,
		Handler:           telemetry.HTTPMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		telemetry.LogAudit(context.Background(), "server.listen", "ok", slog.String("addr", addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[FATAL] Server listen error: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	telemetry.LogAudit(context.Background(), "server.shutdown", "starting")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		telemetry.LogAudit(context.Background(), "server.shutdown", "error", slog.String("error", err.Error()))
	}
	telemetry.LogAudit(context.Background(), "server.shutdown", "ok")
}
