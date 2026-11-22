package queue

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func TestTraceContextPropagationThroughAMQPHeaders(t *testing.T) {
	var traceBuf bytes.Buffer
	tp, shutdown, err := telemetry.InitTracer(context.Background(), "queue-test", "", &traceBuf)
	if err != nil {
		t.Fatalf("InitTracer failed: %v", err)
	}
	defer shutdown(context.Background())

	apiCtx, apiSpan := tp.Tracer("api").Start(context.Background(), "api.enqueue")
	origTraceID := apiSpan.SpanContext().TraceID().String()

	headers := InjectTraceContext(apiCtx)
	if headers["traceparent"] == nil || headers["traceparent"] == "" {
		t.Fatalf("expected traceparent header in AMQP table, got %#v", headers)
	}

	workerBaseCtx := ExtractTraceContext(context.Background(), headers)
	workerCtx, workerSpan := tp.Tracer("worker").Start(workerBaseCtx, "worker.consume")
	gotTraceID := trace.SpanContextFromContext(workerCtx).TraceID().String()

	workerSpan.End()
	apiSpan.End()

	if gotTraceID != origTraceID {
		t.Fatalf("trace_id mismatch across queue boundary: want %s got %s", origTraceID, gotTraceID)
	}
}

func TestRetryBackoffBoundedExponential(t *testing.T) {
	expected := []time.Duration{
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second,
		30 * time.Second,
	}
	for i, want := range expected {
		if got := RetryBackoff(i); got != want {
			t.Errorf("RetryBackoff(%d) = %v, want %v", i, got, want)
		}
	}
	if got := RetryBackoff(-1); got != 2*time.Second {
		t.Errorf("RetryBackoff(-1) = %v, want 2s", got)
	}
}

func TestJobMessageJSONContract(t *testing.T) {
	batchSize := 5000
	orig := JobMessage{
		JobID:             "job-1",
		ExecutionID:       "exec-1",
		RequestID:         "req-1",
		DatabaseID:        "db-1",
		MigrationID:       "mig-1",
		ActionType:        "up",
		ExecutionMode:     "batch",
		BatchSizeOverride: &batchSize,
		RequestedBy:       "user-1",
		AttemptNumber:     1,
		RetryCount:        0,
		EnqueuedAt:        time.Now().UTC().Truncate(time.Second),
	}
	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var decoded JobMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.JobID != orig.JobID || decoded.ExecutionID != orig.ExecutionID || decoded.BatchSizeOverride == nil || *decoded.BatchSizeOverride != 5000 {
		t.Fatalf("unexpected decoded job message: %#v", decoded)
	}
}

type sampleDriver struct{}
type sampleConn struct{}
type sampleTx struct{}

func (sampleDriver) Open(string) (driver.Conn, error)                  { return &sampleConn{}, nil }
func (*sampleConn) Prepare(string) (driver.Stmt, error)                { return nil, errors.New("unused") }
func (*sampleConn) Close() error                                       { return nil }
func (*sampleConn) Begin() (driver.Tx, error)                          { return &sampleTx{}, nil }
func (*sampleConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) { return &sampleTx{}, nil }
func (*sampleConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(500), nil
}
func (*sampleTx) Commit() error   { return nil }
func (*sampleTx) Rollback() error { return nil }

func init() { sql.Register("queue_sample_pg", sampleDriver{}) }

func TestEndToEndTracePropagationAPIToWorkerSQL(t *testing.T) {
	var logBuf, spanBuf bytes.Buffer
	telemetry.InitLogger(&logBuf)
	tp, shutdown, err := telemetry.InitTracer(context.Background(), "db-platform-api", "", &spanBuf)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = shutdown(context.Background())
		telemetry.InitLogger(io.Discard)
	}()

	// 1. Span HTTP Enqueue pada API
	apiCtx, apiSpan := tp.Tracer("api").Start(context.Background(), "HTTP POST /api/v1/requests/req-1/enqueue",
		trace.WithAttributes(attribute.String("http.method", "POST")),
	)
	apiCtx = telemetry.WithActor(apiCtx, "editor_core")
	apiCtx = telemetry.WithDatabase(apiCtx, "target-postgres-1")
	apiCtx = telemetry.WithJobMeta(apiCtx, telemetry.JobLogMeta{
		MigrationID: "mig-001",
		JobID:       "job-001",
		ExecutionID: "exec-001",
		Attempt:     1,
		BatchNumber: 1,
	})
	telemetry.LogAudit(apiCtx, "migration.request.enqueue", "pending_queue",
		slog.String("dsn_debug", "postgres://postgres:TopSecret123@127.0.0.1:5433/target_db"),
	)

	// 2. Pengiriman queue meneruskan traceparent W3C lewat header AMQP
	pubCtx, pubSpan := tp.Tracer("queue").Start(apiCtx, "queue.publish")
	headers := InjectTraceContext(pubCtx)
	pubSpan.End()
	apiSpan.End()

	// 3. Worker membaca header lalu menjalankan lock dan transaksi SQL batch terinstrumentasi
	workerBase := ExtractTraceContext(context.Background(), headers)
	workerBase = telemetry.WithActor(workerBase, "editor_core")
	workerBase = telemetry.WithDatabase(workerBase, "target-postgres-1")
	workerBase = telemetry.WithJobMeta(workerBase, telemetry.JobLogMeta{
		MigrationID: "mig-001",
		JobID:       "job-001",
		ExecutionID: "exec-001",
		Attempt:     1,
		BatchNumber: 1,
	})
	workerCtx, workerSpan := tp.Tracer("worker").Start(workerBase, "worker.process_job")
	lockCtx, lockSpan := tp.Tracer("lock").Start(workerCtx, "lock.acquire")
	telemetry.LogAudit(lockCtx, "lock.acquire", "ok", slog.String("worker_id", "worker-1"))
	lockSpan.End()

	db, err := sql.Open("queue_sample_pg", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	idb := telemetry.WrapDB(db, "target-postgres-1")
	itx, err := idb.BeginTx(workerCtx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := itx.ExecContext(workerCtx, "UPDATE orders SET status = $1 WHERE id > $2 AND id <= $3", "archived", 0, 500); err != nil {
		t.Fatal(err)
	}
	if err := itx.Commit(); err != nil {
		t.Fatal(err)
	}
	workerSpan.End()
	_ = tp.ForceFlush(context.Background())

	if strings.Contains(logBuf.String(), "TopSecret123") {
		t.Fatal("password leaked in structured logs")
	}
	traceID := apiSpan.SpanContext().TraceID().String()
	if strings.Count(logBuf.String(), traceID) < 5 {
		t.Fatalf("expected shared trace_id %s across API, Lock, and SQL logs; got:\n%s", traceID, logBuf.String())
	}
	if strings.Count(spanBuf.String(), traceID) < 5 {
		t.Fatalf("expected shared trace_id %s across OTel console spans; got:\n%s", traceID, spanBuf.String())
	}
	t.Logf("=== SAMPLE_JSON_LOGS ===\n%s=== SAMPLE_OTEL_SPANS ===\n%s", logBuf.String(), spanBuf.String())
}
