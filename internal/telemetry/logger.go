package telemetry

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/trace"
)

type ctxKey string

const (
	ctxKeyActor         ctxKey = "telemetry_actor"
	ctxKeyDatabase      ctxKey = "telemetry_database"
	ctxKeyCorrelationID ctxKey = "telemetry_correlation_id"
	ctxKeyJobMeta       ctxKey = "telemetry_job_meta"
)

// JobLogMeta menyimpan metadata korelasi migrasi, job, eksekusi, attempt, dan batch.
type JobLogMeta struct {
	MigrationID string
	JobID       string
	ExecutionID string
	Attempt     int
	BatchNumber int
}

var (
	loggerMu sync.RWMutex
	logger   = newJSONLogger(os.Stdout)
)

func newJSONLogger(w io.Writer) *slog.Logger {
	if w == nil {
		w = os.Stdout
	}
	opts := &slog.HandlerOptions{
		Level: slog.LevelInfo,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if IsSensitiveKey(a.Key) {
				return slog.String(a.Key, RedactedValue)
			}
			if a.Value.Kind() == slog.KindString {
				return slog.String(a.Key, RedactString(a.Value.String()))
			}
			return a
		},
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

// InitLogger menyiapkan logger JSON terstruktur ke stdout/stderr.
func InitLogger(w io.Writer) *slog.Logger {
	l := newJSONLogger(w)
	loggerMu.Lock()
	logger = l
	loggerMu.Unlock()
	slog.SetDefault(l)
	return l
}

// Logger mengembalikan logger JSON aktif.
func Logger() *slog.Logger {
	loggerMu.RLock()
	l := logger
	loggerMu.RUnlock()
	return l
}

func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, ctxKeyActor, actor)
}

func ActorFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(ctxKeyActor).(string); ok {
		return v
	}
	return ""
}

func WithDatabase(ctx context.Context, database string) context.Context {
	return context.WithValue(ctx, ctxKeyDatabase, database)
}

func DatabaseFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(ctxKeyDatabase).(string); ok {
		return v
	}
	return ""
}

func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyCorrelationID, id)
}

func CorrelationIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(ctxKeyCorrelationID).(string); ok {
		return v
	}
	return ""
}

func WithJobMeta(ctx context.Context, meta JobLogMeta) context.Context {
	return context.WithValue(ctx, ctxKeyJobMeta, meta)
}

func JobMetaFromContext(ctx context.Context) JobLogMeta {
	if ctx == nil {
		return JobLogMeta{}
	}
	if v, ok := ctx.Value(ctxKeyJobMeta).(JobLogMeta); ok {
		return v
	}
	return JobLogMeta{}
}

// TraceIDsFromContext mengambil trace_id dan span_id dari span OpenTelemetry aktif.
func TraceIDsFromContext(ctx context.Context) (string, string) {
	if ctx == nil {
		return "", ""
	}
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return "", ""
	}
	return sc.TraceID().String(), sc.SpanID().String()
}

// LogEvent menulis log aktivitas atau audit JSON dengan seluruh atribut wajib.
func LogEvent(ctx context.Context, level slog.Level, event, status string, extra ...slog.Attr) {
	if ctx == nil {
		ctx = context.Background()
	}
	actor := ActorFromContext(ctx)
	if actor == "" {
		actor = "system"
	}
	database := DatabaseFromContext(ctx)
	corrID := CorrelationIDFromContext(ctx)
	traceID, spanID := TraceIDsFromContext(ctx)
	if corrID == "" && traceID != "" {
		corrID = traceID
	}

	event = strings.ReplaceAll(strings.ReplaceAll(event, "\r", ""), "\n", " ")
	actor = strings.ReplaceAll(strings.ReplaceAll(actor, "\r", ""), "\n", " ")
	database = strings.ReplaceAll(strings.ReplaceAll(database, "\r", ""), "\n", " ")
	status = strings.ReplaceAll(strings.ReplaceAll(status, "\r", ""), "\n", " ")
	corrID = strings.ReplaceAll(strings.ReplaceAll(corrID, "\r", ""), "\n", " ")
	traceID = strings.ReplaceAll(strings.ReplaceAll(traceID, "\r", ""), "\n", " ")
	spanID = strings.ReplaceAll(strings.ReplaceAll(spanID, "\r", ""), "\n", " ")

	attrs := make([]slog.Attr, 0, 12+len(extra))
	attrs = append(attrs,
		slog.String("event", event),
		slog.String("actor", actor),
		slog.String("database", database),
		slog.String("status", status),
		slog.String("correlation_id", corrID),
		slog.String("trace_id", traceID),
		slog.String("span_id", spanID),
	)

	meta := JobMetaFromContext(ctx)
	if meta.MigrationID != "" {
		attrs = append(attrs, slog.String("migration_id", SanitizeLogString(meta.MigrationID)))
	}
	if meta.JobID != "" {
		attrs = append(attrs, slog.String("job_id", SanitizeLogString(meta.JobID)))
	}
	if meta.ExecutionID != "" {
		attrs = append(attrs, slog.String("execution_id", SanitizeLogString(meta.ExecutionID)))
	}
	if meta.Attempt > 0 {
		attrs = append(attrs, slog.Int("attempt", meta.Attempt))
	}
	if meta.BatchNumber > 0 {
		attrs = append(attrs, slog.Int("batch_number", meta.BatchNumber))
	}

	for _, attr := range extra {
		if IsSensitiveKey(attr.Key) {
			attrs = append(attrs, slog.String(attr.Key, RedactedValue))
		} else if attr.Value.Kind() == slog.KindString {
			clean := strings.ReplaceAll(strings.ReplaceAll(attr.Value.String(), "\r", ""), "\n", " ")
			attrs = append(attrs, slog.String(attr.Key, clean))
		} else {
			attrs = append(attrs, attr)
		}
	}

	Logger().LogAttrs(ctx, level, event, attrs...)
}

// LogAudit mencatat event audit level info.
func LogAudit(ctx context.Context, event, status string, extra ...slog.Attr) {
	lvl := slog.LevelInfo
	if status == "error" || status == "failed" || status == "denied" {
		lvl = slog.LevelWarn
	}
	LogEvent(ctx, lvl, event, status, extra...)
}
