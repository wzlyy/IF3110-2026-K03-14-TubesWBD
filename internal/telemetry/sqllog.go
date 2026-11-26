package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// InstrumentedDB membungkus *sql.DB untuk log SQL terpisah dan child span OpenTelemetry.
type InstrumentedDB struct {
	DB       *sql.DB
	Database string
}

// WrapDB membuat InstrumentedDB untuk database target tertentu.
func WrapDB(db *sql.DB, databaseName string) *InstrumentedDB {
	return &InstrumentedDB{DB: db, Database: databaseName}
}

// ExtractSQLState mengambil kode SQLSTATE PostgreSQL jika tersedia.
func ExtractSQLState(err error) string {
	if err == nil {
		return ""
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func (d *InstrumentedDB) withDBContext(ctx context.Context) context.Context {
	if d.Database != "" && DatabaseFromContext(ctx) == "" {
		return WithDatabase(ctx, d.Database)
	}
	return ctx
}

// BeginTx memulai transaksi terinstrumentasi beserta child span OTel.
func (d *InstrumentedDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*InstrumentedTx, error) {
	ctx = d.withDBContext(ctx)
	ctx, span := Tracer().Start(ctx, "db.tx",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.name", d.Database),
			attribute.Bool("db.tx.read_only", opts != nil && opts.ReadOnly),
		),
	)
	started := time.Now()
	LogEvent(ctx, slog.LevelInfo, "sql.tx_start", "started",
		slog.Bool("read_only", opts != nil && opts.ReadOnly),
	)

	tx, err := d.DB.BeginTx(ctx, opts)
	if err != nil {
		sqlState := ExtractSQLState(err)
		RecordSpanError(span, err)
		span.End()
		LogEvent(ctx, slog.LevelError, "sql.tx_start", "error",
			slog.Float64("duration_ms", durationMS(started)),
			slog.String("error", RedactString(err.Error())),
			slog.String("sqlstate", sqlState),
		)
		return nil, err
	}

	return &InstrumentedTx{
		Tx:       tx,
		Database: d.Database,
		ctx:      ctx,
		span:     span,
		started:  started,
	}, nil
}

// ExecContext menjalankan statement di luar transaksi dengan log start dan result.
func (d *InstrumentedDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	ctx = d.withDBContext(ctx)
	return execLogged(ctx, d.Database, "exec", query, args, func(c context.Context) (sql.Result, error) {
		return d.DB.ExecContext(c, query, args...)
	})
}

// QueryContext menjalankan query di luar transaksi dengan log start dan result.
func (d *InstrumentedDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	ctx = d.withDBContext(ctx)
	return queryLogged(ctx, d.Database, "query", query, args, func(c context.Context) (*sql.Rows, error) {
		return d.DB.QueryContext(c, query, args...)
	})
}

// QueryRowContext menjalankan query satu baris dengan log start dan result.
func (d *InstrumentedDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	ctx = d.withDBContext(ctx)
	ctx, span := startSQLSpan(ctx, d.Database, "query_row", query)
	started := time.Now()
	LogEvent(ctx, slog.LevelInfo, "sql.start", "started",
		slog.String("operation", "query_row"),
		slog.String("query", RedactString(query)),
		slog.Any("params", RedactSQLParams(query, args)),
	)
	row := d.DB.QueryRowContext(ctx, query, args...)
	err := row.Err()
	logSQLResult(ctx, span, "query_row", query, args, started, -1, err)
	span.End()
	return row
}

// InstrumentedTx membungkus *sql.Tx agar tiap statement serta Commit/Rollback tercatat terpisah.
type InstrumentedTx struct {
	Tx       *sql.Tx
	Database string
	ctx      context.Context
	span     trace.Span
	started  time.Time
	ended    bool
}

func (t *InstrumentedTx) withCtx(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = t.ctx
	}
	if t.Database != "" && DatabaseFromContext(ctx) == "" {
		ctx = WithDatabase(ctx, t.Database)
	}
	if traceID, _ := TraceIDsFromContext(ctx); traceID == "" && t.ctx != nil {
		if sc := trace.SpanContextFromContext(t.ctx); sc.IsValid() {
			ctx = trace.ContextWithSpanContext(ctx, sc)
		}
	}
	return ctx
}

// ExecContext menjalankan SQL dalam transaksi dengan log sql.start dan sql.result terpisah.
func (t *InstrumentedTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	ctx = t.withCtx(ctx)
	return execLogged(ctx, t.Database, "tx_exec", query, args, func(c context.Context) (sql.Result, error) {
		return t.Tx.ExecContext(c, query, args...)
	})
}

// QueryContext menjalankan query baca dalam transaksi dengan log sql.start dan sql.result terpisah.
func (t *InstrumentedTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	ctx = t.withCtx(ctx)
	return queryLogged(ctx, t.Database, "tx_query", query, args, func(c context.Context) (*sql.Rows, error) {
		return t.Tx.QueryContext(c, query, args...)
	})
}

// QueryRowContext menjalankan query satu baris dalam transaksi.
func (t *InstrumentedTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	ctx = t.withCtx(ctx)
	ctx, span := startSQLSpan(ctx, t.Database, "tx_query_row", query)
	started := time.Now()
	LogEvent(ctx, slog.LevelInfo, "sql.start", "started",
		slog.String("operation", "tx_query_row"),
		slog.String("query", RedactString(query)),
		slog.Any("params", RedactSQLParams(query, args)),
	)
	row := t.Tx.QueryRowContext(ctx, query, args...)
	logSQLResult(ctx, span, "tx_query_row", query, args, started, -1, row.Err())
	span.End()
	return row
}

// PrepareContext menyiapkan statement dalam transaksi dengan log start dan result.
func (t *InstrumentedTx) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	ctx = t.withCtx(ctx)
	ctx, span := startSQLSpan(ctx, t.Database, "tx_prepare", query)
	defer span.End()
	started := time.Now()
	LogEvent(ctx, slog.LevelInfo, "sql.start", "started",
		slog.String("operation", "tx_prepare"),
		slog.String("query", RedactString(query)),
	)
	stmt, err := t.Tx.PrepareContext(ctx, query)
	logSQLResult(ctx, span, "tx_prepare", query, nil, started, -1, err)
	return stmt, err
}

// QueryRowPrepared menjalankan prepared query satu baris (mis. EXPLAIN) dengan log dan span OTel.
func (t *InstrumentedTx) QueryRowPrepared(ctx context.Context, stmt *sql.Stmt, query string, dest ...any) error {
	ctx = t.withCtx(ctx)
	ctx, span := startSQLSpan(ctx, t.Database, "tx_stmt_query", query)
	defer span.End()
	started := time.Now()
	LogEvent(ctx, slog.LevelInfo, "sql.start", "started",
		slog.String("operation", "tx_stmt_query"),
		slog.String("query", RedactString(query)),
	)
	err := stmt.QueryRowContext(ctx).Scan(dest...)
	logSQLResult(ctx, span, "tx_stmt_query", query, nil, started, -1, err)
	return err
}

// Commit menyelesaikan transaksi dan mencatat event sql.tx_end terpisah.
func (t *InstrumentedTx) Commit() error {
	if t.ended {
		return sql.ErrTxDone
	}
	t.ended = true
	commitStart := time.Now()
	err := t.Tx.Commit()
	sqlState := ExtractSQLState(err)
	status := "committed"
	level := slog.LevelInfo
	if err != nil {
		status = "error"
		level = slog.LevelError
		RecordSpanError(t.span, err)
	}
	t.span.SetAttributes(
		attribute.String("db.tx.action", "commit"),
		attribute.Float64("db.tx.duration_ms", durationMS(t.started)),
	)
	LogEvent(t.ctx, level, "sql.tx_end", status,
		slog.String("tx_action", "commit"),
		slog.Float64("duration_ms", durationMS(commitStart)),
		slog.Float64("tx_total_duration_ms", durationMS(t.started)),
		slog.String("error", errString(err)),
		slog.String("sqlstate", sqlState),
	)
	t.span.End()
	return err
}

// Rollback membatalkan transaksi dan mencatat event sql.tx_end secara idempoten.
func (t *InstrumentedTx) Rollback() error {
	if t.ended {
		return nil
	}
	t.ended = true
	rbStart := time.Now()
	err := t.Tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		t.span.End()
		return nil
	}
	sqlState := ExtractSQLState(err)
	status := "rolled_back"
	level := slog.LevelInfo
	if err != nil {
		status = "error"
		level = slog.LevelError
		RecordSpanError(t.span, err)
	}
	t.span.SetAttributes(
		attribute.String("db.tx.action", "rollback"),
		attribute.Float64("db.tx.duration_ms", durationMS(t.started)),
	)
	LogEvent(t.ctx, level, "sql.tx_end", status,
		slog.String("tx_action", "rollback"),
		slog.Float64("duration_ms", durationMS(rbStart)),
		slog.Float64("tx_total_duration_ms", durationMS(t.started)),
		slog.String("error", errString(err)),
		slog.String("sqlstate", sqlState),
	)
	t.span.End()
	return err
}

func startSQLSpan(ctx context.Context, dbName, op, query string) (context.Context, trace.Span) {
	meta := JobMetaFromContext(ctx)
	attrs := []attribute.KeyValue{
		attribute.String("db.system", "postgresql"),
		attribute.String("db.name", dbName),
		attribute.String("db.operation", op),
	}
	if meta.MigrationID != "" {
		attrs = append(attrs, attribute.String("migration.id", meta.MigrationID))
	}
	if meta.JobID != "" {
		attrs = append(attrs, attribute.String("migration.job_id", meta.JobID))
	}
	if meta.ExecutionID != "" {
		attrs = append(attrs, attribute.String("migration.execution_id", meta.ExecutionID))
	}
	if meta.Attempt > 0 {
		attrs = append(attrs, attribute.Int("migration.attempt", meta.Attempt))
	}
	if meta.BatchNumber > 0 {
		attrs = append(attrs, attribute.Int("migration.batch_number", meta.BatchNumber))
	}
	_ = query // Jaga atribut span tetap ringkas; SQL penuh ditulis lewat log JSON.
	return Tracer().Start(ctx, "db.sql."+op,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...),
	)
}

func execLogged(ctx context.Context, dbName, op, query string, args []any, fn func(context.Context) (sql.Result, error)) (sql.Result, error) {
	ctx, span := startSQLSpan(ctx, dbName, op, query)
	defer span.End()

	started := time.Now()
	LogEvent(ctx, slog.LevelInfo, "sql.start", "started",
		slog.String("operation", op),
		slog.String("query", RedactString(query)),
		slog.Any("params", RedactSQLParams(query, args)),
	)

	res, err := fn(ctx)
	rowsAffected := int64(-1)
	if err == nil && res != nil {
		if n, rerr := res.RowsAffected(); rerr == nil {
			rowsAffected = n
		}
	}
	logSQLResult(ctx, span, op, query, args, started, rowsAffected, err)
	return res, err
}

func queryLogged(ctx context.Context, dbName, op, query string, args []any, fn func(context.Context) (*sql.Rows, error)) (*sql.Rows, error) {
	ctx, span := startSQLSpan(ctx, dbName, op, query)
	defer span.End()

	started := time.Now()
	LogEvent(ctx, slog.LevelInfo, "sql.start", "started",
		slog.String("operation", op),
		slog.String("query", RedactString(query)),
		slog.Any("params", RedactSQLParams(query, args)),
	)

	rows, err := fn(ctx)
	logSQLResult(ctx, span, op, query, args, started, -1, err)
	return rows, err
}

func logSQLResult(ctx context.Context, span trace.Span, op, query string, args []any, started time.Time, rowsAffected int64, err error) {
	durMS := durationMS(started)
	sqlState := ExtractSQLState(err)
	status := "ok"
	level := slog.LevelInfo
	if err != nil {
		status = "error"
		level = slog.LevelError
		RecordSpanError(span, err)
	}
	span.SetAttributes(attribute.Float64("db.duration_ms", durMS))
	if rowsAffected >= 0 {
		span.SetAttributes(attribute.Int64("db.rows_affected", rowsAffected))
	}

	attrs := []slog.Attr{
		slog.String("operation", op),
		slog.String("query", RedactString(query)),
		slog.Any("params", RedactSQLParams(query, args)),
		slog.Float64("duration_ms", durMS),
	}
	if rowsAffected >= 0 {
		attrs = append(attrs, slog.Int64("rows_affected", rowsAffected))
	}
	if err != nil {
		attrs = append(attrs,
			slog.String("error", RedactString(err.Error())),
			slog.String("sqlstate", sqlState),
		)
	}
	LogEvent(ctx, level, "sql.result", status, attrs...)
}

func durationMS(started time.Time) float64 {
	return float64(time.Since(started).Microseconds()) / 1000.0
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return RedactString(err.Error())
}
