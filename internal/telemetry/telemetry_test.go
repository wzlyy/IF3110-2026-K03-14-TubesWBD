package telemetry

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

func TestRedactionMasksSecretsAndDSNs(t *testing.T) {
	if !IsSensitiveKey("password_encrypted") || !IsSensitiveKey("csrf_token") || !IsSensitiveKey("session_id") {
		t.Fatal("expected sensitive keys to be flagged")
	}
	if IsSensitiveKey("database") || IsSensitiveKey("migration_id") {
		t.Fatal("unexpected sensitive key flag on non-secret keys")
	}

	raw := "failed dialing postgres://admin:SuperSecret99@db.local:5432/prod?sslmode=disable with Bearer eyJhbGciOiJIUzI1NiJ9 and session_id=abc123def"
	clean := RedactString(raw)
	for _, secret := range []string{"SuperSecret99", "eyJhbGciOiJIUzI1NiJ9", "abc123def"} {
		if strings.Contains(clean, secret) {
			t.Fatalf("secret %q leaked into redacted output: %s", secret, clean)
		}
	}
	if !strings.Contains(clean, "REDACTED") {
		t.Fatalf("expected REDACTED marker in %q", clean)
	}

	params := RedactParams([]any{"normal", "password=mysecret", []byte{1, 2, 3}})
	if len(params) != 3 || strings.Contains(params[1].(string), "mysecret") || params[2] != "<3 bytes>" {
		t.Fatalf("unexpected redacted params: %#v", params)
	}

	// Pastikan karakter CR/LF dibersihkan untuk mencegah log injection.
	forged := SanitizeLogString("alice\r\nfake_entry=true")
	if strings.ContainsAny(forged, "\r\n") {
		t.Fatalf("CR/LF tidak terhapus pada SanitizeLogString: %q", forged)
	}
}

func TestStructuredLoggingWithOTelTraceAndSQLThreeEvents(t *testing.T) {
	var logBuf, traceBuf bytes.Buffer
	InitLogger(&logBuf)
	tp, shutdown, err := InitTracer(context.Background(), "test-service", "", &traceBuf)
	if err != nil {
		t.Fatalf("InitTracer failed: %v", err)
	}
	defer func() {
		_ = shutdown(context.Background())
		InitLogger(io.Discard)
	}()

	// Daftarkan driver SQL palsu sekali untuk unit test terisolasi.
	db := openStubSQLDB(t)
	defer db.Close()

	ctx, rootSpan := tp.Tracer("test").Start(context.Background(), "api.test.request")
	ctx = WithActor(ctx, "dika_editor")
	ctx = WithDatabase(ctx, "target-postgres-1")
	ctx = WithCorrelationID(ctx, "corr-123")
	ctx = WithJobMeta(ctx, JobLogMeta{
		MigrationID: "mig-1",
		JobID:       "job-1",
		ExecutionID: "exec-1",
		Attempt:     1,
		BatchNumber: 2,
	})

	idb := WrapDB(db, "target-postgres-1")
	itx, err := idb.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("BeginTx failed: %v", err)
	}
	rows, err := itx.QueryContext(ctx, "SELECT id FROM orders WHERE token = $1", "secret_token=xyz")
	if err != nil {
		t.Fatalf("QueryContext failed: %v", err)
	}
	_ = rows.Close()

	// Uji ekstraksi kode error SQLSTATE pada statement yang gagal.
	_, err = itx.ExecContext(ctx, "FAIL_SQLSTATE")
	if err == nil {
		t.Fatal("expected SQL error on FAIL_SQLSTATE")
	}
	if err := itx.Rollback(); err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}
	rootSpan.End()
	_ = tp.ForceFlush(context.Background())

	lines := strings.Split(strings.TrimSpace(logBuf.String()), "\n")
	events := make(map[string]int)
	var sawSQLState bool
	for _, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line is not valid JSON: %s (%v)", line, err)
		}
		ev, _ := entry["event"].(string)
		events[ev]++
		if entry["actor"] != "dika_editor" || entry["database"] != "target-postgres-1" || entry["correlation_id"] != "corr-123" {
			t.Fatalf("missing mandatory log attributes in %s", line)
		}
		if entry["trace_id"] == "" || entry["span_id"] == "" {
			t.Fatalf("expected non-empty trace_id/span_id in %s", line)
		}
		if strings.Contains(line, "xyz") {
			t.Fatalf("sensitive param leaked in %s", line)
		}
		if entry["sqlstate"] == "42601" {
			sawSQLState = true
		}
	}

	for _, required := range []string{"sql.tx_start", "sql.start", "sql.result", "sql.tx_end"} {
		if events[required] == 0 {
			t.Fatalf("expected event %q in structured logs, got %#v", required, events)
		}
	}
	if !sawSQLState {
		t.Fatalf("expected sqlstate 42601 to be recorded in error log, logs=%s", logBuf.String())
	}
	if !strings.Contains(traceBuf.String(), "db.sql.tx_query") || !strings.Contains(traceBuf.String(), "db.tx") {
		t.Fatalf("expected OTel console spans for SQL & TX, got: %s", traceBuf.String())
	}
}

func TestHTTPMiddlewareSetsTraceHeaders(t *testing.T) {
	var traceBuf bytes.Buffer
	_, shutdown, err := InitTracer(context.Background(), "http-test", "", &traceBuf)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdown(context.Background())

	h := HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if CorrelationIDFromContext(r.Context()) == "" {
			t.Error("missing correlation_id in context")
		}
		w.WriteHeader(http.StatusAccepted)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Header().Get("X-Trace-ID") == "" || rec.Header().Get("X-Correlation-ID") == "" {
		t.Fatalf("expected X-Trace-ID and X-Correlation-ID headers, got %v", rec.Header())
	}

	// Pastikan status 500 menandai span sebagai error.
	errH := HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	errRec := httptest.NewRecorder()
	errH.ServeHTTP(errRec, httptest.NewRequest(http.MethodGet, "/api/v1/boom", nil))
	if errRec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", errRec.Code)
	}
}

func TestInstrumentedTxQueryRowPreparedAndCommit(t *testing.T) {
	var logBuf bytes.Buffer
	InitLogger(&logBuf)
	defer InitLogger(io.Discard)

	db := openStubSQLDB(t)
	defer db.Close()

	idb := WrapDB(db, "target-postgres-1")
	itx, err := idb.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("BeginTx failed: %v", err)
	}
	stmt, err := itx.PrepareContext(context.Background(), "EXPLAIN (FORMAT JSON) SELECT 1")
	if err != nil {
		t.Fatalf("PrepareContext failed: %v", err)
	}
	defer stmt.Close()

	var plan string
	if err := itx.QueryRowPrepared(context.Background(), stmt, "EXPLAIN (FORMAT JSON) SELECT 1", &plan); err != nil {
		t.Fatalf("QueryRowPrepared failed: %v", err)
	}
	if err := itx.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	out := logBuf.String()
	for _, want := range []string{`"operation":"tx_prepare"`, `"operation":"tx_stmt_query"`, `"tx_action":"commit"`, `"status":"committed"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %s in structured logs, got:\n%s", want, out)
		}
	}
}

func TestMetricsDBBackedInflightGauges(t *testing.T) {
	db := openStubSQLDB(t)
	defer db.Close()

	m := NewMetrics()
	m.RegisterJobInflightDB(db)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `migration_jobs_inflight{status="waiting"} 4`) ||
		!strings.Contains(body, `migration_jobs_inflight{status="running"} 2`) {
		t.Fatalf("expected DB-backed waiting=4 and running=2 gauges, got:\n%s", body)
	}
}

func TestMetricsExposeRequiredLowCardinalityMetrics(t *testing.T) {
	m := NewMetrics()
	m.RegisterDBPool("platform", func() sql.DBStats {
		return sql.DBStats{InUse: 3, Idle: 7, WaitCount: 2, WaitDuration: 150 * time.Millisecond}
	})
	m.RegisterDBPool("target_postgres", func() sql.DBStats {
		return sql.DBStats{InUse: 1, Idle: 2, WaitCount: 0, WaitDuration: 0}
	})
	m.RecordJobEnqueued("up", "batch")
	m.RecordJobStarted("up", "batch", 250*time.Millisecond)
	m.ObserveLockWait(40 * time.Millisecond)
	m.RecordBatchProcessed("committed", 500)
	m.RecordJobRetry("up")
	m.RecordJobFinished("up", "batch", "success", 2*time.Second)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics handler status = %d", rec.Code)
	}

	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("invalid prometheus exposition format: %v", err)
	}

	required := []string{
		"migration_jobs_total",
		"migration_jobs_inflight",
		"migration_job_retries_total",
		"migration_queue_wait_seconds",
		"migration_lock_wait_seconds",
		"migration_execution_duration_seconds",
		"migration_batches_processed_total",
		"migration_rows_processed_total",
		"db_pool_connections",
		"db_pool_wait_count_total",
		"db_pool_wait_duration_seconds_total",
	}
	forbiddenLabels := map[string]bool{
		"user_id": true, "job_id": true, "execution_id": true, "query": true, "sql": true,
	}

	for _, name := range required {
		fam, ok := families[name]
		if !ok {
			t.Errorf("missing required Prometheus metric %q", name)
			continue
		}
		for _, metric := range fam.GetMetric() {
			for _, lp := range metric.GetLabel() {
				if forbiddenLabels[lp.GetName()] {
					t.Errorf("metric %q uses forbidden high-cardinality label %q", name, lp.GetName())
				}
			}
		}
	}

	bodyText := rec.Body.String()
	for _, wantRole := range []string{`db_role="platform"`, `db_role="target_postgres"`} {
		if !strings.Contains(bodyText, wantRole) {
			t.Errorf("expected Prometheus pool label %s in /metrics output:\n%s", wantRole, bodyText)
		}
	}
}

// --- Stub sql/driver minimal untuk unit test terisolasi ---

type stubDriver struct{}
type stubConn struct{}
type stubStmt struct{}
type stubTx struct{}
type stubRows struct {
	cols []string
	data [][]driver.Value
	idx  int
}

func (stubDriver) Open(string) (driver.Conn, error) { return &stubConn{}, nil }
func (c *stubConn) Prepare(string) (driver.Stmt, error) {
	return &stubStmt{}, nil
}
func (c *stubConn) Close() error              { return nil }
func (c *stubConn) Begin() (driver.Tx, error) { return &stubTx{}, nil }
func (c *stubConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return &stubTx{}, nil
}
func (c *stubConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "FROM migration_requests") {
		return &stubRows{
			cols: []string{"status", "count"},
			data: [][]driver.Value{
				{"pending_queue", int64(4)},
				{"running", int64(2)},
			},
		}, nil
	}
	return &stubRows{
		cols: []string{"id"},
		data: [][]driver.Value{{int64(1)}},
	}, nil
}
func (c *stubConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if query == "FAIL_SQLSTATE" {
		return nil, &pgconn.PgError{Code: "42601", Message: "syntax error"}
	}
	return driver.RowsAffected(1), nil
}
func (*stubStmt) Close() error                                    { return nil }
func (*stubStmt) NumInput() int                                   { return 0 }
func (*stubStmt) Exec([]driver.Value) (driver.Result, error)      { return driver.RowsAffected(1), nil }
func (*stubStmt) Query([]driver.Value) (driver.Rows, error) {
	return &stubRows{cols: []string{"plan"}, data: [][]driver.Value{{"[{\"Plan\":{}}]"}}}, nil
}
func (*stubTx) Commit() error             { return nil }
func (*stubTx) Rollback() error           { return nil }
func (r *stubRows) Columns() []string     { return r.cols }
func (r *stubRows) Close() error          { return nil }
func (r *stubRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.idx])
	r.idx++
	return nil
}

func init() {
	sql.Register("telemetry_stub_pg", stubDriver{})
}

func openStubSQLDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("telemetry_stub_pg", "")
	if err != nil {
		t.Fatalf("open stub sql db: %v", err)
	}
	return db
}

func TestLoggerErrorRedactionSQLParamRedactionPGXTracerAndInflightCache(t *testing.T) {
	var buf bytes.Buffer
	oldLogger := Logger()
	InitLogger(&buf)
	t.Cleanup(func() {
		loggerMu.Lock()
		logger = oldLogger
		loggerMu.Unlock()
		slog.SetDefault(oldLogger)
	})

	// 1. slog.Any("error", err) harus disanitasi & disamarkan menjadi string tunggal.
	LogAudit(context.Background(), "test.err", "error",
		slog.Any("error", errors.New("dial postgres://user:Secret99@db/app\ninjected=1")),
	)
	var errEntry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &errEntry); err != nil {
		t.Fatalf("unmarshal log json: %v (%s)", err, buf.String())
	}
	errVal, ok := errEntry["error"].(string)
	if !ok || strings.Contains(errVal, "Secret99") || strings.Contains(errVal, "\n") || !strings.Contains(errVal, "REDACTED") {
		t.Fatalf("expected redacted sanitized error string, got %#v", errEntry["error"])
	}

	// 2. RedactSQLParams menyamarkan seluruh parameter pada tabel/kolom sensitif.
	sessParams := RedactSQLParams("SELECT user_id FROM sessions WHERE id = $1 AND csrf_token = $2", []any{"tok-123", "csrf-456"})
	if len(sessParams) != 2 || sessParams[0] != RedactedValue || sessParams[1] != RedactedValue {
		t.Fatalf("expected redacted session params, got %#v", sessParams)
	}
	ordParams := RedactSQLParams("UPDATE orders SET status = $1 WHERE id = $2", []any{"archived", 7})
	if len(ordParams) != 2 || ordParams[0] != "archived" || ordParams[1] != 7 {
		t.Fatalf("unexpected non-sensitive params: %#v", ordParams)
	}

	// 3. NewPGXTracer memaksa label database="platform", memfilter argumen internal pgx, serta melacak tx & query.
	buf.Reset()
	tr := NewPGXTracer("platform")
	ctx := WithDatabase(context.Background(), "target-1")
	txCtx := tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "BEGIN"})
	tr.TraceQueryEnd(txCtx, nil, pgx.TraceQueryEndData{})
	qCtx := tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{
		SQL:  "SELECT user_id FROM sessions WHERE id = $1",
		Args: []any{pgx.QueryExecModeSimpleProtocol, "secret-session-tok"},
	})
	tr.TraceQueryEnd(qCtx, nil, pgx.TraceQueryEndData{})
	commitCtx := tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "COMMIT"})
	tr.TraceQueryEnd(commitCtx, nil, pgx.TraceQueryEndData{})

	logText := buf.String()
	if strings.Contains(logText, "secret-session-tok") || strings.Contains(logText, "simple_protocol") {
		t.Fatalf("pgx tracer leaked secret or internal pgx arg: %s", logText)
	}
	for _, want := range []string{`"database":"platform"`, `"sql.tx_start"`, `"sql.start"`, `"sql.result"`, `"sql.tx_end"`, `"committed"`} {
		if !strings.Contains(logText, want) {
			t.Fatalf("expected %s in pgx tracer logs:\n%s", want, logText)
		}
	}

	// 4. Metrics.Handler melakukan cache count inflight DB selama inflightCacheTTL.
	m := NewMetrics()
	calls := 0
	m.poolMu.Lock()
	m.inflightSource = func(context.Context) (float64, float64, bool) {
		calls++
		return 3, 1, true
	}
	m.poolMu.Unlock()
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `migration_jobs_inflight{status="waiting"} 3`) {
			t.Fatalf("scrape %d unexpected response %d:\n%s", i, rec.Code, rec.Body.String())
		}
	}
	if calls != 1 {
		t.Fatalf("expected cached inflight query count = 1 across rapid scrapes, got %d", calls)
	}
}

func TestRecordSpanErrorRedactsCredentials(t *testing.T) {
	var traceBuf bytes.Buffer
	tp, shutdown, err := InitTracer(context.Background(), "test-service", "", &traceBuf)
	if err != nil {
		t.Fatalf("InitTracer failed: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	_, span := tp.Tracer("test").Start(context.Background(), "worker.consume")
	RecordSpanError(span, errors.New("dial postgres://postgres:TopSecret123@db:5432/app failed"))
	span.End()
	_ = tp.ForceFlush(context.Background())

	out := traceBuf.String()
	if strings.Contains(out, "TopSecret123") {
		t.Fatalf("span output leaked raw secret: %s", out)
	}
	if !strings.Contains(out, "REDACTED") || !strings.Contains(out, `"Code":"Error"`) {
		t.Fatalf("expected redacted error and Error status on span: %s", out)
	}
}
