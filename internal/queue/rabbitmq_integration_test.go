//go:build integration

package queue_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/queue"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/telemetry"
	"go.opentelemetry.io/otel/trace"
)

func TestRabbitMQDurablePublishTracePropagationAndRetryIntegration(t *testing.T) {
	if os.Getenv("WBD_TEST_DATABASES") != "1" {
		t.Skip("use scripts/test-integration.ps1 with dedicated test RabbitMQ")
	}
	rmqURL := os.Getenv("RABBITMQ_URL")
	if rmqURL == "" {
		rmqURL = "amqp://guest:guest@127.0.0.1:15673/"
	}

	var traceBuf bytes.Buffer
	tp, shutdown, err := telemetry.InitTracer(context.Background(), "rmq-integration-test", "", &traceBuf)
	if err != nil {
		t.Fatalf("InitTracer: %v", err)
	}
	defer shutdown(context.Background())

	client, err := queue.NewRabbitMQClient(rmqURL)
	if err != nil {
		t.Fatalf("NewRabbitMQClient(%s): %v", rmqURL, err)
	}
	defer client.Close()

	ch, err := client.Channel()
	if err != nil {
		t.Fatalf("Channel: %v", err)
	}
	// Kosongkan sisa pesan uji sebelum memulai verifikasi.
	_, _ = ch.QueuePurge(queue.QueueJobs, false)
	_, _ = ch.QueuePurge(queue.QueueRetry, false)
	_, _ = ch.QueuePurge(queue.QueueDLQ, false)

	// 1. Kirim job persisten dengan span OpenTelemetry aktif.
	apiCtx, apiSpan := tp.Tracer("api").Start(context.Background(), "api.enqueue.integration")
	origTraceID := apiSpan.SpanContext().TraceID().String()
	apiCtx = telemetry.WithCorrelationID(apiCtx, "corr-rmq-int")

	job := queue.JobMessage{
		JobID:         uuid.NewString(),
		ExecutionID:   uuid.NewString(),
		RequestID:     uuid.NewString(),
		DatabaseID:    "db-1",
		MigrationID:   "mig-1",
		ActionType:    "up",
		ExecutionMode: "normal",
		RequestedBy:   "tester",
		AttemptNumber: 1,
		EnqueuedAt:    time.Now().UTC(),
	}
	if err := client.PublishJob(apiCtx, job); err != nil {
		t.Fatalf("PublishJob: %v", err)
	}
	apiSpan.End()

	// 2. Konsumsi dari migration.jobs lalu verifikasi payload dan trace_id OTel.
	deliveries, err := ch.Consume(queue.QueueJobs, "int-test-consumer", false, false, false, false, nil)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}

	select {
	case d := <-deliveries:
		var got queue.JobMessage
		if err := json.Unmarshal(d.Body, &got); err != nil {
			t.Fatalf("unmarshal delivery: %v", err)
		}
		if got.JobID != job.JobID || got.ExecutionID != job.ExecutionID {
			t.Fatalf("unexpected job payload: got=%+v want=%+v", got, job)
		}
		workerCtx := queue.ExtractTraceContext(context.Background(), d.Headers)
		gotTraceID := trace.SpanContextFromContext(workerCtx).TraceID().String()
		if gotTraceID != origTraceID {
			t.Fatalf("propagated trace_id = %s, want %s (headers=%#v)", gotTraceID, origTraceID, d.Headers)
		}
		if err := d.Ack(false); err != nil {
			t.Fatalf("Ack: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for job message from RabbitMQ")
	}

	// 3. Verifikasi PublishRetry mengembalikan pesan ke migration.jobs setelah jeda TTL (300ms).
	retryJob := job
	retryJob.JobID = uuid.NewString()
	retryJob.RetryCount = 1
	if err := client.PublishRetry(context.Background(), retryJob, 300*time.Millisecond); err != nil {
		t.Fatalf("PublishRetry: %v", err)
	}

	select {
	case d := <-deliveries:
		var got queue.JobMessage
		if err := json.Unmarshal(d.Body, &got); err != nil {
			t.Fatalf("unmarshal retry delivery: %v", err)
		}
		if got.JobID != retryJob.JobID || got.RetryCount != 1 {
			t.Fatalf("unexpected retry payload: %+v", got)
		}
		_ = d.Ack(false)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for delayed retry redelivery from migration.jobs.retry -> migration.jobs")
	}
}
