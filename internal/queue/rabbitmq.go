package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

var ErrQueueUnavailable = errors.New("message queue unavailable")

// RabbitMQClient mengelola topologi RabbitMQ durable dan pengiriman job persisten.
type RabbitMQClient struct {
	url  string
	mu   sync.Mutex
	conn *amqp.Connection
	ch   *amqp.Channel
}

// NewRabbitMQClient terhubung ke RabbitMQ dan mendeklarasikan exchange serta queue durable.
func NewRabbitMQClient(url string) (*RabbitMQClient, error) {
	c := &RabbitMQClient{url: url}
	if err := c.connect(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *RabbitMQClient) connect() error {
	conn, err := amqp.DialConfig(c.url, amqp.Config{
		Dial: amqp.DefaultDial(5 * time.Second),
	})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrQueueUnavailable, telemetry.RedactString(err.Error()))
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("%w: open channel failed", ErrQueueUnavailable)
	}
	if err := DeclareTopology(ch); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return err
	}
	if err := ch.Confirm(false); err != nil {
		_ = conn.Close()
		return fmt.Errorf("enable publisher confirms: %w", err)
	}
	c.conn = conn
	c.ch = ch
	return nil
}

// DeclareTopology membuat exchange durable, queue utama, queue retry TTL, dan DLQ.
// Dipakai bersama oleh publisher API Go dan consumer Worker Go.
func DeclareTopology(ch *amqp.Channel) error {
	if err := ch.ExchangeDeclare(
		ExchangeName,
		"direct",
		true,  // Persisten (durable)
		false, // Tidak otomatis dihapus
		false, // Bukan internal
		false, // Tunggu konfirmasi broker
		nil,
	); err != nil {
		return fmt.Errorf("declare exchange %s: %w", ExchangeName, err)
	}

	if _, err := ch.QueueDeclare(
		QueueJobs,
		true,  // Persisten (durable)
		false, // Tidak dihapus saat kosong
		false, // Tidak eksklusif
		false, // Tunggu konfirmasi broker
		amqp.Table{
			"x-dead-letter-exchange":    ExchangeName,
			"x-dead-letter-routing-key": RoutingKeyDLQ,
		},
	); err != nil {
		return fmt.Errorf("declare queue %s: %w", QueueJobs, err)
	}
	if err := ch.QueueBind(QueueJobs, RoutingKeyJobs, ExchangeName, false, nil); err != nil {
		return fmt.Errorf("bind queue %s: %w", QueueJobs, err)
	}

	// Queue retry menahan pesan dengan Expiration per pesan lalu mengembalikannya ke QueueJobs.
	if _, err := ch.QueueDeclare(
		QueueRetry,
		true,
		false,
		false,
		false,
		amqp.Table{
			"x-dead-letter-exchange":    ExchangeName,
			"x-dead-letter-routing-key": RoutingKeyJobs,
		},
	); err != nil {
		return fmt.Errorf("declare retry queue %s: %w", QueueRetry, err)
	}
	if err := ch.QueueBind(QueueRetry, RoutingKeyRetry, ExchangeName, false, nil); err != nil {
		return fmt.Errorf("bind retry queue %s: %w", QueueRetry, err)
	}

	if _, err := ch.QueueDeclare(
		QueueDLQ,
		true,
		false,
		false,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("declare dlq %s: %w", QueueDLQ, err)
	}
	if err := ch.QueueBind(QueueDLQ, RoutingKeyDLQ, ExchangeName, false, nil); err != nil {
		return fmt.Errorf("bind dlq %s: %w", QueueDLQ, err)
	}
	return nil
}

func (c *RabbitMQClient) ensureChannel() (*amqp.Channel, error) {
	if c.conn == nil || c.conn.IsClosed() || c.ch == nil || c.ch.IsClosed() {
		if c.ch != nil {
			_ = c.ch.Close()
		}
		if c.conn != nil {
			_ = c.conn.Close()
		}
		if err := c.connect(); err != nil {
			return nil, err
		}
	}
	return c.ch, nil
}

// PublishJob mengirim job migrasi persisten ke migration.jobs beserta header trace OTel.
func (c *RabbitMQClient) PublishJob(ctx context.Context, msg JobMessage) error {
	if msg.EnqueuedAt.IsZero() {
		msg.EnqueuedAt = time.Now().UTC()
	}
	return c.publish(ctx, RoutingKeyJobs, msg, 0)
}

// PublishRetry mengirim job yang gagal sementara ke migration.jobs.retry dengan TTL eksponensial.
func (c *RabbitMQClient) PublishRetry(ctx context.Context, msg JobMessage, delay time.Duration) error {
	if delay <= 0 {
		delay = RetryBackoff(msg.RetryCount)
	}
	return c.publish(ctx, RoutingKeyRetry, msg, delay)
}

// PublishDLQ mengirim job yang melebihi batas retry ke migration.jobs.dlq.
func (c *RabbitMQClient) PublishDLQ(ctx context.Context, msg JobMessage) error {
	return c.publish(ctx, RoutingKeyDLQ, msg, 0)
}

func (c *RabbitMQClient) publish(ctx context.Context, routingKey string, msg JobMessage, delay time.Duration) error {
	ctx, span := telemetry.Tracer().Start(ctx, "queue.publish",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "rabbitmq"),
			attribute.String("messaging.destination.name", routingKey),
			attribute.String("migration.job_id", msg.JobID),
			attribute.String("migration.execution_id", msg.ExecutionID),
			attribute.String("migration.action_type", msg.ActionType),
		),
	)
	defer span.End()

	body, err := json.Marshal(msg)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("marshal job message: %w", err)
	}

	headers := InjectTraceContext(ctx)
	if corrID := telemetry.CorrelationIDFromContext(ctx); corrID != "" {
		headers["x-correlation-id"] = corrID
	}

	pub := amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		MessageId:    msg.JobID,
		Timestamp:    msg.EnqueuedAt,
		Headers:      headers,
		Body:         body,
	}
	if delay > 0 {
		pub.Expiration = strconv.FormatInt(delay.Milliseconds(), 10)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	ch, err := c.ensureChannel()
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	confirmation, err := ch.PublishWithDeferredConfirmWithContext(ctx, ExchangeName, routingKey, false, false, pub)
	if err == nil {
		wait, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		var ack bool
		ack, err = confirmation.WaitContext(wait)
		if err == nil && !ack {
			err = errors.New("broker rejected persistent message")
		}
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("%w: publish failed: %v", ErrQueueUnavailable, err)
	}

	telemetry.LogAudit(ctx, "queue.publish", "ok",
		slog.String("routing_key", routingKey),
		slog.String("job_id", msg.JobID),
		slog.String("execution_id", msg.ExecutionID),
		slog.String("action_type", msg.ActionType),
	)
	return nil
}

// Channel mengembalikan channel AMQP internal (untuk pengaturan consumer di worker).
func (c *RabbitMQClient) Channel() (*amqp.Channel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ensureChannel()
}

// Close menutup channel dan koneksi RabbitMQ dengan bersih.
func (c *RabbitMQClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var firstErr error
	if c.ch != nil {
		if err := c.ch.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		c.ch = nil
	}
	if c.conn != nil {
		if err := c.conn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		c.conn = nil
	}
	return firstErr
}
