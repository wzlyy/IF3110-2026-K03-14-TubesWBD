package config

import (
	"strings"
	"testing"
)

func TestEncryptionKeyContract(t *testing.T) {
	for _, key := range []string{"", "short", strings.Repeat("x", 31), strings.Repeat("x", 33)} {
		t.Setenv("ENCRYPTION_KEY", key)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "ENCRYPTION_KEY") {
			t.Fatalf("key length %d: expected configuration error, got %v", len(key), err)
		}
	}
	key := "12345678901234567890123456789012"
	t.Setenv("ENCRYPTION_KEY", key)
	cfg, err := Load()
	if err != nil || cfg.EncryptionKey != key {
		t.Fatalf("valid key rejected: %v", err)
	}
}

func TestProductionSessionCookieCannotBeWeakened(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("x", 32))
	t.Setenv("APP_ENV", "production")
	t.Setenv("COOKIE_SECURE", "false")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.CookieSecure {
		t.Fatal("production cookies must remain Secure even when COOKIE_SECURE=false")
	}
}

func TestMilestone2ConfigDefaultsAndOverrides(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("k", 32))
	t.Setenv("RABBITMQ_URL", "amqp://user:pass@rmq:5672/")
	t.Setenv("OTEL_SERVICE_NAME", "custom-api")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://otel-collector:4318")
	t.Setenv("SPA_DIST_DIR", "/app/frontend/dist")
	t.Setenv("WORKER_ID", "worker-42")
	t.Setenv("METRICS_PORT", "9191")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.RabbitMQURL != "amqp://user:pass@rmq:5672/" ||
		cfg.OTelServiceName != "custom-api" ||
		cfg.OTelOTLPEndpoint != "http://otel-collector:4318" ||
		cfg.SPADistDir != "/app/frontend/dist" ||
		cfg.WorkerID != "worker-42" ||
		cfg.MetricsPort != "9191" {
		t.Fatalf("unexpected M2 config fields: %+v", cfg)
	}
}
