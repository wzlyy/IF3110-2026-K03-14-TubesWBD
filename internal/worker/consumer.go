package worker

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/lock"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/migration"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/queue"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/telemetry"
	"go.opentelemetry.io/otel/trace"
)

func (s *Service) Run(ctx context.Context, url string, concurrency int) error {
	if concurrency < 1 {
		return errors.New("worker concurrency must be positive")
	}
	for ctx.Err() == nil {
		client, e := queue.NewRabbitMQClient(url)
		if e == nil {
			e = s.consume(ctx, client, concurrency)
			client.Close()
		}
		if ctx.Err() != nil {
			return nil
		}
		slog.Warn("reconnecting migration consumer", "error", e)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(2 * time.Second):
		}
	}
	return nil
}

func (s *Service) consume(ctx context.Context, c *queue.RabbitMQClient, n int) error {
	ch, e := c.Channel()
	if e != nil {
		return e
	}
	if e = ch.Qos(n, 0, false); e != nil {
		return e
	}
	deliveries, e := ch.ConsumeWithContext(ctx, queue.QueueJobs, "", false, false, false, false, nil)
	if e != nil {
		return e
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for d := range deliveries {
				s.delivery(ctx, c, d)
			}
		}()
	}
	wg.Wait()
	return nil
}

type retryPublisher interface {
	PublishRetry(ctx context.Context, msg queue.JobMessage, delay time.Duration) error
}

func (s *Service) delivery(base context.Context, c retryPublisher, d amqp.Delivery) {
	var j queue.JobMessage
	if e := json.Unmarshal(d.Body, &j); e != nil || j.JobID == "" || j.ExecutionID == "" || j.RequestID == "" {
		d.Reject(false)
		return
	}
	// Stop accepting deliveries on shutdown and give active transactions a
	// bounded drain period. A forced stop leaves the message unacknowledged.
	drain, cancel := context.WithCancel(context.WithoutCancel(base))
	defer cancel()
	stopDrain := context.AfterFunc(base, func() {
		timer := time.NewTimer(20 * time.Second)
		defer timer.Stop()
		select {
		case <-drain.Done():
		case <-timer.C:
			cancel()
		}
	})
	defer stopDrain()
	ctx := queue.ExtractTraceContext(drain, d.Headers)
	ctx = telemetry.WithCorrelationID(ctx, queue.AMQPHeaderCarrier(d.Headers).Get("x-correlation-id"))
	ctx = telemetry.WithJobMeta(ctx, telemetry.JobLogMeta{MigrationID: j.MigrationID, ExecutionID: j.ExecutionID, JobID: j.JobID, Attempt: j.AttemptNumber})
	dbLabel := j.DatabaseName
	if dbLabel == "" {
		dbLabel = j.DatabaseID
	}
	actor := j.RequestedByUsername
	if actor == "" {
		actor = j.RequestedBy
	}
	ctx = telemetry.WithActor(telemetry.WithDatabase(ctx, dbLabel), actor)
	ctx, span := telemetry.Tracer().Start(ctx, "worker.consume", trace.WithSpanKind(trace.SpanKindConsumer))
	defer span.End()
	attemptAt := time.Now().UTC()
	proc := s.Process
	if s.processFn != nil {
		proc = s.processFn
	}
	err := proc(ctx, j)
	if err == nil {
		d.Ack(false)
		return
	}
	span.RecordError(err)
	telemetry.LogAudit(ctx, "worker.delivery.deferred", "deferred",
		slog.String("action_type", j.ActionType),
		slog.Int("retry_count", j.RetryCount),
		slog.String("error", telemetry.RedactString(err.Error())),
	)
	if base.Err() != nil {
		d.Nack(false, true)
		return
	}
	if errors.Is(err, errInvalidJob) || errors.Is(err, migration.ErrRequestNotFound) || errors.Is(err, migration.ErrDefinitionNotFound) {
		d.Reject(false)
		return
	}
	if errors.Is(err, errVerificationPending) {
		// Checking commit evidence must survive an outage longer than the SQL retry budget.
		if j.LockWaitStartedAt == nil {
			j.LockWaitStartedAt = &attemptAt
		}
		err = c.PublishRetry(ctx, j, lock.DefaultLeaseTTL)
	} else if errors.Is(err, lock.ErrLockBusy) || errors.Is(err, lock.ErrLockNotOwned) || errors.Is(err, lock.ErrLockNotFound) {
		if j.LockWaitStartedAt == nil {
			j.LockWaitStartedAt = &attemptAt
		}
		err = c.PublishRetry(ctx, j, 2*time.Second)
	} else if j.RetryCount < queue.MaxTransientRetries {
		if errors.Is(err, errNextAttempt) {
			j = nextAttempt(j)
			ctx = telemetry.WithJobMeta(ctx, telemetry.JobLogMeta{
				MigrationID: j.MigrationID,
				ExecutionID: j.ExecutionID,
				JobID:       j.JobID,
				Attempt:     j.AttemptNumber,
			})
		}
		delay := queue.RetryBackoff(j.RetryCount)
		j.RetryCount++
		telemetry.GlobalMetrics().RecordJobRetry(j.ActionType)
		err = c.PublishRetry(ctx, j, delay)
	} else {
		// Process records the final SQL failure before returning nil. If platform
		// storage is down, preserve delivery until we can read/write that decision.
		err = c.PublishRetry(ctx, j, lock.DefaultLeaseTTL)
	}
	if err != nil {
		d.Nack(false, true)
		return
	}
	d.Ack(false)
}
