package queue

import (
	"context"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
)

// AMQPHeaderCarrier mengadaptasi amqp.Table ke TextMapCarrier OpenTelemetry
// agar konteks trace mengalir melewati RabbitMQ.
type AMQPHeaderCarrier amqp.Table

func (c AMQPHeaderCarrier) Get(key string) string {
	v, ok := c[key]
	if !ok || v == nil {
		return ""
	}
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	default:
		return fmt.Sprint(s)
	}
}

func (c AMQPHeaderCarrier) Set(key, value string) {
	c[key] = value
}

func (c AMQPHeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

// InjectTraceContext menyisipkan konteks span OpenTelemetry aktif ke header AMQP.
func InjectTraceContext(ctx context.Context) amqp.Table {
	headers := amqp.Table{}
	otel.GetTextMapPropagator().Inject(ctx, AMQPHeaderCarrier(headers))
	return headers
}

// ExtractTraceContext memulihkan konteks trace OpenTelemetry dari header AMQP ke ctx.
func ExtractTraceContext(ctx context.Context, headers amqp.Table) context.Context {
	if headers == nil {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, AMQPHeaderCarrier(headers))
}
