package telemetry

import (
	"context"
	"fmt"
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

	event = strings.ReplaceAll(strings.ReplaceAll(event, "\n", ""), "\r", "")
	actor = strings.ReplaceAll(strings.ReplaceAll(actor, "\n", ""), "\r", "")
	database = strings.ReplaceAll(strings.ReplaceAll(database, "\n", ""), "\r", "")
	status = strings.ReplaceAll(strings.ReplaceAll(status, "\n", ""), "\r", "")
	corrID = strings.ReplaceAll(strings.ReplaceAll(corrID, "\n", ""), "\r", "")
	traceID = strings.ReplaceAll(strings.ReplaceAll(traceID, "\n", ""), "\r", "")
	spanID = strings.ReplaceAll(strings.ReplaceAll(spanID, "\n", ""), "\r", "")

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

	// Rekonstruksi setiap atribut tanpa melewatkan attr mentah agar bebas log injection.
	for _, attr := range extra {
		key := SanitizeLogString(attr.Key)
		if IsSensitiveKey(key) {
			attrs = append(attrs, slog.String(key, RedactedValue))
			continue
		}
		switch attr.Value.Kind() {
		case slog.KindString:
			clean := strings.ReplaceAll(strings.ReplaceAll(attr.Value.String(), "\n", ""), "\r", "")
			attrs = append(attrs, slog.String(key, clean))
		case slog.KindInt64:
			attrs = append(attrs, slog.Int64(key, attr.Value.Int64()))
		case slog.KindUint64:
			attrs = append(attrs, slog.Uint64(key, attr.Value.Uint64()))
		case slog.KindFloat64:
			attrs = append(attrs, slog.Float64(key, attr.Value.Float64()))
		case slog.KindBool:
			attrs = append(attrs, slog.Bool(key, attr.Value.Bool()))
		case slog.KindDuration:
			attrs = append(attrs, slog.Duration(key, attr.Value.Duration()))
		case slog.KindTime:
			attrs = append(attrs, slog.Time(key, attr.Value.Time()))
		default:
			if params, ok := attr.Value.Any().([]any); ok {
				attrs = append(attrs, slog.Any(key, RedactParams(params)))
			} else if attr.Value.Any() != nil {
				clean := strings.ReplaceAll(strings.ReplaceAll(fmt.Sprint(attr.Value.Any()), "\n", ""), "\r", "")
				attrs = append(attrs, slog.String(key, clean))
			} else {
				attrs = append(attrs, slog.Any(key, nil))
			}
		}
	}

	Logger().LogAttrs(ctx, level, event, attrs...)
}

// LogAudit mencatat event audit level info.
func LogAudit(ctx context.Context, event, status string, extra ...slog.Attr) {
	lvl := slog.LevelInfo
	if status == "error" || status == "failed" || status == "denied" || status == "deferred" {
		lvl = slog.LevelWarn
	}
	LogEvent(ctx, lvl, event, status, extra...)
}
