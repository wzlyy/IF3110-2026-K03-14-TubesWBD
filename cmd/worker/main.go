package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/config"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/lock"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/telemetry"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/worker"
)

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	telemetry.InitLogger(os.Stdout)
	cfg, e := config.Load()
	if e != nil {
		return e
	}
	srv := os.Getenv("OTEL_SERVICE_NAME")
	if srv == "" {
		srv = "migration-worker"
	}
	_, shutdown, e := telemetry.InitTracer(ctx, srv, cfg.OTelOTLPEndpoint, os.Stdout)
	if e != nil {
		return e
	}
	defer func() {
		flush, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdown(flush)
	}()
	telemetry.LogAudit(ctx, "worker.startup", "starting",
		slog.String("service", srv),
		slog.String("worker_id", cfg.WorkerID),
	)
	db, e := database.NewPlatformDB(cfg)
	if e != nil {
		return e
	}
	defer db.Close()
	store, e := database.NewConnectionStore(db, cfg.EncryptionKey)
	if e != nil {
		return e
	}
	targets := database.NewTargetManager(store)
	defer targets.Close()
	metrics := telemetry.GlobalMetrics()
	metrics.RegisterDBPool("platform", db.Stats)
	metrics.RegisterDBPool("target_postgres", targets.AggregatePoolStats)
	metrics.RegisterJobInflightDB(db)
	addr := os.Getenv("WORKER_METRICS_ADDR")
	if addr == "" {
		addr = ":" + cfg.MetricsPort
	}
	telemetry.StartMetricsServer(ctx, addr, metrics)
	telemetry.LogAudit(ctx, "worker.metrics.listen", "ok", slog.String("addr", addr))
	concurrency := 4
	if v := os.Getenv("WORKER_CONCURRENCY"); v != "" {
		concurrency, e = strconv.Atoi(v)
		if e != nil || concurrency < 1 {
			return errors.New("WORKER_CONCURRENCY must be positive")
		}
	}
	telemetry.LogAudit(ctx, "worker.listen", "ok",
		slog.String("worker_id", cfg.WorkerID),
		slog.Int("concurrency", concurrency),
	)
	svc := worker.Service{DB: db, Targets: targets, Locks: lock.NewManager(db), WorkerID: cfg.WorkerID}
	return svc.Run(ctx, cfg.RabbitMQURL, concurrency)
}

func main() {
	if e := run(); e != nil {
		telemetry.LogEvent(context.Background(), slog.LevelError, "worker.stop", "error",
			slog.String("error", telemetry.RedactString(e.Error())),
		)
		os.Exit(1)
	}
}
