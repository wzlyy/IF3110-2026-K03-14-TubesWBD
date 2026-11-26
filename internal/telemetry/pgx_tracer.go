package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type pgxTraceCtxKey struct{}

type pgxTraceState struct {
	ctx     context.Context
	span    trace.Span
	started time.Time
	kind    string
	action  string
	query   string
	args    []any
}

// PGXTracer menginstrumentasi koneksi pgx (khususnya Platform DB) dengan log SQL terstruktur dan span OTel.
type PGXTracer struct {
	Database string
}

// NewPGXTracer membuat QueryTracer pgx yang memberi label nama database secara eksplisit.
func NewPGXTracer(databaseName string) *PGXTracer {
	if strings.TrimSpace(databaseName) == "" {
		databaseName = "platform"
	}
	return &PGXTracer{Database: databaseName}
}

// TraceQueryStart mencatat awal eksekusi query atau kendali transaksi pgx.
func (t *PGXTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	// Selalu paksa konteks database agar query Platform DB selama eksekusi target tidak salah berlabel target.
	ctx = WithDatabase(ctx, t.Database)
	started := time.Now()
	kind, action, readOnly := classifyPGXStatement(data.SQL)

	switch kind {
	case "tx_start":
		spanCtx, span := Tracer().Start(ctx, "db.tx",
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				attribute.String("db.system", "postgresql"),
				attribute.String("db.name", t.Database),
				attribute.Bool("db.tx.read_only", readOnly),
			),
		)
		LogEvent(spanCtx, slog.LevelInfo, "sql.tx_start", "started",
			slog.Bool("read_only", readOnly),
		)
		return context.WithValue(spanCtx, pgxTraceCtxKey{}, &pgxTraceState{
			ctx:     spanCtx,
			span:    span,
			started: started,
			kind:    kind,
		})

	case "tx_end":
		spanCtx, span := Tracer().Start(ctx, "db.tx",
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				attribute.String("db.system", "postgresql"),
				attribute.String("db.name", t.Database),
				attribute.String("db.tx.action", action),
			),
		)
		return context.WithValue(spanCtx, pgxTraceCtxKey{}, &pgxTraceState{
			ctx:     spanCtx,
			span:    span,
			started: started,
			kind:    kind,
			action:  action,
		})

	default:
		args := filterPGXArgs(data.Args)
		spanCtx, span := startSQLSpan(ctx, t.Database, "platform_query", data.SQL)
		LogEvent(spanCtx, slog.LevelInfo, "sql.start", "started",
			slog.String("operation", "platform_query"),
			slog.String("query", RedactString(data.SQL)),
			slog.Any("params", RedactSQLParams(data.SQL, args)),
		)
		return context.WithValue(spanCtx, pgxTraceCtxKey{}, &pgxTraceState{
			ctx:     spanCtx,
			span:    span,
			started: started,
			kind:    "query",
			query:   data.SQL,
			args:    args,
		})
	}
}

// TraceQueryEnd menyelesaikan span dan menulis log hasil query atau transaksi pgx.
func (t *PGXTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	st, ok := ctx.Value(pgxTraceCtxKey{}).(*pgxTraceState)
	if !ok || st == nil {
		return
	}
	defer st.span.End()

	switch st.kind {
	case "tx_start":
		if data.Err != nil {
			sqlState := ExtractSQLState(data.Err)
			st.span.RecordError(errors.New(RedactString(data.Err.Error())))
			st.span.SetStatus(codes.Error, RedactString(data.Err.Error()))
			LogEvent(st.ctx, slog.LevelError, "sql.tx_start", "error",
				slog.Float64("duration_ms", durationMS(st.started)),
				slog.String("error", RedactString(data.Err.Error())),
				slog.String("sqlstate", sqlState),
			)
		}

	case "tx_end":
		status := "committed"
		if st.action == "rollback" {
			status = "rolled_back"
		}
		level := slog.LevelInfo
		if data.Err != nil {
			status = "error"
			level = slog.LevelError
			st.span.RecordError(errors.New(RedactString(data.Err.Error())))
			st.span.SetStatus(codes.Error, RedactString(data.Err.Error()))
		}
		st.span.SetAttributes(
			attribute.String("db.tx.action", st.action),
			attribute.Float64("db.tx.duration_ms", durationMS(st.started)),
		)
		LogEvent(st.ctx, level, "sql.tx_end", status,
			slog.String("tx_action", st.action),
			slog.Float64("duration_ms", durationMS(st.started)),
			slog.String("error", errString(data.Err)),
			slog.String("sqlstate", ExtractSQLState(data.Err)),
		)

	default:
		rowsAffected := int64(-1)
		if data.Err == nil && (data.CommandTag.Insert() || data.CommandTag.Update() || data.CommandTag.Delete()) {
			rowsAffected = data.CommandTag.RowsAffected()
		}
		logSQLResult(st.ctx, st.span, "platform_query", st.query, st.args, st.started, rowsAffected, data.Err)
	}
}

func classifyPGXStatement(rawSQL string) (kind, action string, readOnly bool) {
	upper := strings.ToUpper(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rawSQL), ";")))
	switch {
	case upper == "BEGIN" || strings.HasPrefix(upper, "BEGIN ") ||
		upper == "START TRANSACTION" || strings.HasPrefix(upper, "START TRANSACTION "):
		return "tx_start", "begin", strings.Contains(upper, "READ ONLY")
	case upper == "COMMIT" || strings.HasPrefix(upper, "COMMIT "):
		return "tx_end", "commit", false
	case upper == "ROLLBACK" || (strings.HasPrefix(upper, "ROLLBACK ") && !strings.HasPrefix(upper, "ROLLBACK TO")):
		return "tx_end", "rollback", false
	default:
		return "query", "platform_query", false
	}
}

func filterPGXArgs(args []any) []any {
	if len(args) == 0 {
		return nil
	}
	out := make([]any, 0, len(args))
	for _, a := range args {
		switch a.(type) {
		case pgx.QueryExecMode, pgx.QueryResultFormats, pgx.QueryResultFormatsByOID, pgx.QueryRewriter:
			continue
		default:
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
