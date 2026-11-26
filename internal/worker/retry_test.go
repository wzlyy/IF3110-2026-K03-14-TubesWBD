package worker

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/lock"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/migration"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/queue"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/telemetry"
)

func TestRetryKeepsJobAndCreatesOneAuditIDPerAttempt(t *testing.T) {
	original := queue.JobMessage{JobID: "job", ExecutionID: "execution", AttemptNumber: 1}
	first := nextAttempt(original)
	if first.JobID != original.JobID || first.AttemptNumber != 2 || first.ExecutionID == original.ExecutionID {
		t.Fatalf("unexpected retry: %+v", first)
	}
	if nextAttempt(original).ExecutionID != first.ExecutionID || nextAttempt(first).ExecutionID == first.ExecutionID {
		t.Fatal("redelivery must reuse the same ID, a subsequent attempt must get a new ID")
	}
}

func TestUnavailableProofKeepsVerificationPending(t *testing.T) {
	ctx := t.Context()
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	db, err := sql.Open("pgx", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = (&Service{DB: db}).verify(ctx, "db", "execution", "lease")
	if !errors.Is(err, errVerificationPending) || !errors.Is(err, context.Canceled) {
		t.Fatalf("verification lost its retry or cause: %v", err)
	}
}

func TestAttemptWithTelemetryUsesResolvedNames(t *testing.T) {
	base := telemetry.WithActor(telemetry.WithDatabase(t.Context(), "db-uuid"), "user-uuid")
	resolved := (&attempt{dbName: "target-postgres-1", actor: "editor_core"}).withTelemetry(base)
	if got := telemetry.DatabaseFromContext(resolved); got != "target-postgres-1" {
		t.Fatalf("DatabaseFromContext = %q, want target-postgres-1", got)
	}
	if got := telemetry.ActorFromContext(resolved); got != "editor_core" {
		t.Fatalf("ActorFromContext = %q, want editor_core", got)
	}

	fallback := (&attempt{}).withTelemetry(base)
	if got := telemetry.DatabaseFromContext(fallback); got != "db-uuid" {
		t.Fatalf("fallback DatabaseFromContext = %q, want db-uuid", got)
	}
	if got := telemetry.ActorFromContext(fallback); got != "user-uuid" {
		t.Fatalf("fallback ActorFromContext = %q, want user-uuid", got)
	}
}

type fakeNetError struct {
	msg       string
	timeout   bool
	temporary bool
}

func (e fakeNetError) Error() string   { return e.msg }
func (e fakeNetError) Timeout() bool   { return e.timeout }
func (e fakeNetError) Temporary() bool { return e.temporary }

func TestIsTransient(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "net error", err: fakeNetError{msg: "dial tcp timeout", timeout: true}, want: true},
		{name: "io EOF", err: io.EOF, want: true},
		{name: "bad conn", err: driver.ErrBadConn, want: true},
		{name: "pg 08006", err: &pgconn.PgError{Code: "08006", Message: "connection failure"}, want: true},
		{name: "pg 08001", err: &pgconn.PgError{Code: "08001", Message: "unable to establish connection"}, want: true},
		{name: "pg 57P01", err: &pgconn.PgError{Code: "57P01", Message: "admin shutdown"}, want: true},
		{name: "pg 40001", err: &pgconn.PgError{Code: "40001", Message: "serialization failure"}, want: true},
		{name: "pg 40P01", err: &pgconn.PgError{Code: "40P01", Message: "deadlock detected"}, want: true},
		{name: "connect text", err: errors.New("failed to connect to database"), want: true},
		{name: "koneksi text", err: errors.New("koneksi database terputus"), want: true},
		{name: "syntax 42601", err: &pgconn.PgError{Code: "42601", Message: "syntax error"}, want: false},
		{name: "unique 23505", err: &pgconn.PgError{Code: "23505", Message: "duplicate key"}, want: false},
		{name: "check 23514", err: &pgconn.PgError{Code: "23514", Message: "check constraint"}, want: false},
		{name: "generic error", err: errors.New("permission denied"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransient(tc.err); got != tc.want {
				t.Fatalf("isTransient(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

type fakeAck struct {
	acks    int
	nacks   []bool
	rejects []bool
}

func (a *fakeAck) Ack(_ uint64, _ bool) error {
	a.acks++
	return nil
}

func (a *fakeAck) Nack(_ uint64, _, requeue bool) error {
	a.nacks = append(a.nacks, requeue)
	return nil
}

func (a *fakeAck) Reject(_ uint64, requeue bool) error {
	a.rejects = append(a.rejects, requeue)
	return nil
}

type retryCall struct {
	msg   queue.JobMessage
	delay time.Duration
	meta  telemetry.JobLogMeta
}

type fakeRetryPub struct {
	calls []retryCall
	err   error
}

func (p *fakeRetryPub) PublishRetry(ctx context.Context, msg queue.JobMessage, delay time.Duration) error {
	p.calls = append(p.calls, retryCall{
		msg:   msg,
		delay: delay,
		meta:  telemetry.JobMetaFromContext(ctx),
	})
	return p.err
}

func assertUniqueJSONKeys(t *testing.T, raw []byte) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	var checkObj func()
	var skipVal func()
	skipVal = func() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("json token error in %s: %v", string(raw), err)
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{':
				checkObj()
			case '[':
				for dec.More() {
					skipVal()
				}
				if _, err := dec.Token(); err != nil {
					t.Fatalf("json array close error: %v", err)
				}
			}
		}
	}
	checkObj = func() {
		seen := make(map[string]bool)
		for dec.More() {
			kTok, err := dec.Token()
			if err != nil {
				t.Fatalf("json key error: %v", err)
			}
			key, ok := kTok.(string)
			if !ok {
				t.Fatalf("expected string key, got %T", kTok)
			}
			if seen[key] {
				t.Fatalf("duplicate JSON key %q in log line: %s", key, string(raw))
			}
			seen[key] = true
			skipVal()
		}
		if _, err := dec.Token(); err != nil {
			t.Fatalf("json object close error: %v", err)
		}
	}
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		t.Fatalf("expected JSON object in %s, got %v err=%v", string(raw), tok, err)
	}
	checkObj()
}

func TestDeliveryRetryDecisionsAndStructuredLogs(t *testing.T) {
	var logBuf bytes.Buffer
	telemetry.InitLogger(&logBuf)
	tp, shutdown, err := telemetry.InitTracer(context.Background(), "worker-retry-test", "", io.Discard)
	if err != nil {
		t.Fatalf("InitTracer: %v", err)
	}
	defer func() {
		_ = shutdown(context.Background())
		telemetry.InitLogger(io.Discard)
	}()

	apiCtx, apiSpan := tp.Tracer("api").Start(context.Background(), "enqueue")
	headers := queue.InjectTraceContext(apiCtx)
	headers["x-correlation-id"] = "corr-delivery-1"
	apiSpan.End()

	makeDelivery := func(j queue.JobMessage, ack *fakeAck) amqp.Delivery {
		b, err := json.Marshal(j)
		if err != nil {
			t.Fatal(err)
		}
		return amqp.Delivery{
			Acknowledger: ack,
			Headers:      headers,
			Body:         b,
		}
	}

	baseJob := queue.JobMessage{
		JobID:               "job-1",
		ExecutionID:         "exec-1",
		RequestID:           "req-1",
		DatabaseID:          "db-1",
		DatabaseName:        "target-postgres-1",
		MigrationID:         "mig-1",
		ActionType:          "up",
		ExecutionMode:       "batch",
		RequestedBy:         "user-1",
		RequestedByUsername: "editor_core",
		AttemptNumber:       1,
		EnqueuedAt:          time.Now().UTC(),
	}

	t.Run("malformed payload rejects without requeue", func(t *testing.T) {
		ack := &fakeAck{}
		svc := Service{processFn: func(context.Context, queue.JobMessage) error {
			t.Fatal("process should not run for invalid payload")
			return nil
		}}
		svc.delivery(context.Background(), &fakeRetryPub{}, amqp.Delivery{Acknowledger: ack, Body: []byte(`{"job_id":""}`)})
		if len(ack.rejects) != 1 || ack.rejects[0] {
			t.Fatalf("rejects = %v, want [false]", ack.rejects)
		}
	})

	t.Run("success acks without retry", func(t *testing.T) {
		ack := &fakeAck{}
		pub := &fakeRetryPub{}
		svc := Service{processFn: func(context.Context, queue.JobMessage) error { return nil }}
		svc.delivery(context.Background(), pub, makeDelivery(baseJob, ack))
		if ack.acks != 1 || len(pub.calls) != 0 {
			t.Fatalf("acks=%d retries=%d, want 1 ack and 0 retries", ack.acks, len(pub.calls))
		}
	})

	for _, permanentErr := range []error{errInvalidJob, migration.ErrRequestNotFound, migration.ErrDefinitionNotFound} {
		t.Run("permanent metadata error rejects: "+permanentErr.Error(), func(t *testing.T) {
			ack := &fakeAck{}
			pub := &fakeRetryPub{}
			svc := Service{processFn: func(context.Context, queue.JobMessage) error { return permanentErr }}
			svc.delivery(context.Background(), pub, makeDelivery(baseJob, ack))
			if len(ack.rejects) != 1 || ack.rejects[0] || len(pub.calls) != 0 {
				t.Fatalf("rejects=%v retries=%d, want [false] and 0 retries", ack.rejects, len(pub.calls))
			}
		})
	}

	t.Run("verification pending uses lease TTL beyond transient retry budget and preserves lock wait start", func(t *testing.T) {
		ack := &fakeAck{}
		pub := &fakeRetryPub{}
		j := baseJob
		j.RetryCount = queue.MaxTransientRetries + 2
		svc := Service{processFn: func(context.Context, queue.JobMessage) error { return errVerificationPending }}
		svc.delivery(context.Background(), pub, makeDelivery(j, ack))
		if ack.acks != 1 || len(pub.calls) != 1 {
			t.Fatalf("acks=%d retries=%d", ack.acks, len(pub.calls))
		}
		call := pub.calls[0]
		if call.delay != lock.DefaultLeaseTTL {
			t.Fatalf("delay = %v, want %v", call.delay, lock.DefaultLeaseTTL)
		}
		if call.msg.LockWaitStartedAt == nil {
			t.Fatal("expected LockWaitStartedAt to be initialized")
		}
		if call.msg.RetryCount != j.RetryCount {
			t.Fatalf("RetryCount changed from %d to %d", j.RetryCount, call.msg.RetryCount)
		}

		// Subsequent redelivery preserves the original LockWaitStartedAt.
		firstWait := *call.msg.LockWaitStartedAt
		time.Sleep(5 * time.Millisecond)
		ack2 := &fakeAck{}
		svc.delivery(context.Background(), pub, makeDelivery(call.msg, ack2))
		if !pub.calls[1].msg.LockWaitStartedAt.Equal(firstWait) {
			t.Fatalf("LockWaitStartedAt overwritten: got %v, want %v", pub.calls[1].msg.LockWaitStartedAt, firstWait)
		}
	})

	t.Run("lock contention retries every 2s and preserves first wait timestamp", func(t *testing.T) {
		ack := &fakeAck{}
		pub := &fakeRetryPub{}
		svc := Service{processFn: func(context.Context, queue.JobMessage) error { return lock.ErrLockBusy }}
		svc.delivery(context.Background(), pub, makeDelivery(baseJob, ack))
		if ack.acks != 1 || len(pub.calls) != 1 {
			t.Fatalf("acks=%d retries=%d", ack.acks, len(pub.calls))
		}
		if pub.calls[0].delay != 2*time.Second || pub.calls[0].msg.LockWaitStartedAt == nil {
			t.Fatalf("unexpected lock-busy retry: %+v", pub.calls[0])
		}
		firstWait := *pub.calls[0].msg.LockWaitStartedAt
		time.Sleep(5 * time.Millisecond)
		svc.delivery(context.Background(), pub, makeDelivery(pub.calls[0].msg, &fakeAck{}))
		if !pub.calls[1].msg.LockWaitStartedAt.Equal(firstWait) {
			t.Fatalf("lock wait timestamp changed across lock retries: %v vs %v", pub.calls[1].msg.LockWaitStartedAt, firstWait)
		}
	})

	t.Run("transient execution retry uses bounded backoff, new attempt ID, and updates log meta", func(t *testing.T) {
		logBuf.Reset()
		pub := &fakeRetryPub{}
		svc := Service{processFn: func(context.Context, queue.JobMessage) error { return errNextAttempt }}
		oldWait := time.Now().Add(-10 * time.Second).UTC()
		cur := baseJob
		cur.LockWaitStartedAt = &oldWait
		wantDelays := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}
		for i, wantDelay := range wantDelays {
			ack := &fakeAck{}
			prevExec := cur.ExecutionID
			svc.delivery(context.Background(), pub, makeDelivery(cur, ack))
			if ack.acks != 1 {
				t.Fatalf("step %d acks=%d", i, ack.acks)
			}
			call := pub.calls[i]
			if call.delay != wantDelay {
				t.Fatalf("step %d delay = %v, want %v", i, call.delay, wantDelay)
			}
			if call.msg.ExecutionID == prevExec || call.msg.AttemptNumber != cur.AttemptNumber+1 || call.msg.RetryCount != i+1 {
				t.Fatalf("step %d unexpected attempt/retry progression: %+v", i, call.msg)
			}
			if call.msg.LockWaitStartedAt != nil {
				t.Fatalf("step %d expected LockWaitStartedAt reset on new attempt", i)
			}
			if call.meta.ExecutionID != call.msg.ExecutionID || call.meta.Attempt != call.msg.AttemptNumber {
				t.Fatalf("step %d ctx JobLogMeta not updated to next attempt: meta=%+v msg=%+v", i, call.meta, call.msg)
			}
			cur = call.msg
		}

		// Exceeding MaxTransientRetries falls back to DefaultLeaseTTL without calling nextAttempt.
		ackFinal := &fakeAck{}
		prevExec := cur.ExecutionID
		svc.delivery(context.Background(), pub, makeDelivery(cur, ackFinal))
		last := pub.calls[len(pub.calls)-1]
		if last.delay != lock.DefaultLeaseTTL || last.msg.ExecutionID != prevExec || last.msg.AttemptNumber != cur.AttemptNumber {
			t.Fatalf("retry budget exceeded should keep execution and wait DefaultLeaseTTL: %+v", last)
		}
	})

	t.Run("shutdown cancellation and publish failure nack with requeue", func(t *testing.T) {
		canceledCtx, cancel := context.WithCancel(context.Background())
		cancel()
		ackCancel := &fakeAck{}
		pubCancel := &fakeRetryPub{}
		svcCancel := Service{processFn: func(context.Context, queue.JobMessage) error { return lock.ErrLockBusy }}
		svcCancel.delivery(canceledCtx, pubCancel, makeDelivery(baseJob, ackCancel))
		if len(ackCancel.nacks) != 1 || !ackCancel.nacks[0] || len(pubCancel.calls) != 0 {
			t.Fatalf("canceled delivery nacks=%v retries=%d", ackCancel.nacks, len(pubCancel.calls))
		}

		ackErr := &fakeAck{}
		pubErr := &fakeRetryPub{err: errors.New("broker down")}
		svcErr := Service{processFn: func(context.Context, queue.JobMessage) error { return lock.ErrLockBusy }}
		svcErr.delivery(context.Background(), pubErr, makeDelivery(baseJob, ackErr))
		if len(ackErr.nacks) != 1 || !ackErr.nacks[0] || ackErr.acks != 0 {
			t.Fatalf("publish failure delivery nacks=%v acks=%d", ackErr.nacks, ackErr.acks)
		}
	})

	// Every structured log line emitted by delivery() must carry mandatory attributes without duplicate JSON keys.
	lines := strings.Split(strings.TrimSpace(logBuf.String()), "\n")
	if len(lines) == 0 {
		t.Fatal("expected structured logs from delivery()")
	}
	var sawDeferred bool
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		assertUniqueJSONKeys(t, []byte(line))
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("invalid json log %s: %v", line, err)
		}
		for _, k := range []string{"event", "actor", "database", "correlation_id", "trace_id", "span_id", "job_id", "execution_id", "attempt"} {
			v, ok := entry[k]
			if !ok || v == "" || v == float64(0) {
				t.Fatalf("missing or empty mandatory log field %q in %s", k, line)
			}
		}
		if entry["event"] == "worker.delivery.deferred" {
			sawDeferred = true
			if entry["status"] != "deferred" || entry["level"] != "WARN" {
				t.Fatalf("unexpected deferred log status/level in %s", line)
			}
		}
	}
	if !sawDeferred {
		t.Fatalf("expected worker.delivery.deferred audit event in logs: %s", logBuf.String())
	}
}
