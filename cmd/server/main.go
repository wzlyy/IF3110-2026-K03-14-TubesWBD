package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/api"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/config"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/lock"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/queue"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/telemetry"
)

func registerFrontend(mux *http.ServeMux, distDir string) {
	mux.Handle("GET /", api.NewSPAHandler(distDir))
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

	registerFrontend(mux, cfg.SPADistDir)

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
