package telemetry

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.30.0"
	"go.opentelemetry.io/otel/trace"
)

const InstrumentationName = "github.com/wzlyy/IF3110-2026-K03-14-TubesWBD"

// InitTracer menyiapkan TracerProvider OpenTelemetry global dan propagator W3C.
// Jika otlpEndpoint diisi, trace dikirim via OTLP/HTTP; jika kosong, ditulis ke stdout.
func InitTracer(ctx context.Context, serviceName, otlpEndpoint string, stdoutWriter io.Writer) (*sdktrace.TracerProvider, func(context.Context) error, error) {
	if strings.TrimSpace(serviceName) == "" {
		serviceName = "db-platform-api"
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create otel resource: %w", err)
	}

	var exporter sdktrace.SpanExporter
	if ep := strings.TrimSpace(otlpEndpoint); ep != "" {
		opts := []otlptracehttp.Option{otlptracehttp.WithInsecure()}
		if strings.HasPrefix(ep, "http://") || strings.HasPrefix(ep, "https://") {
			opts = []otlptracehttp.Option{otlptracehttp.WithEndpointURL(ep)}
		} else {
			opts = append(opts, otlptracehttp.WithEndpoint(ep))
		}
		exporter, err = otlptracehttp.New(ctx, opts...)
		if err != nil {
			return nil, nil, fmt.Errorf("create otlp trace exporter: %w", err)
		}
	} else {
		if stdoutWriter == nil {
			stdoutWriter = os.Stdout
		}
		exporter, err = stdouttrace.New(stdouttrace.WithWriter(stdoutWriter))
		if err != nil {
			return nil, nil, fmt.Errorf("create stdout trace exporter: %w", err)
		}
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	shutdown := func(shutdownCtx context.Context) error {
		return tp.Shutdown(shutdownCtx)
	}
	return tp, shutdown, nil
}

// Tracer mengembalikan tracer OpenTelemetry bersama.
func Tracer() trace.Tracer {
	return otel.Tracer(InstrumentationName)
}

type statusCapturingWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusCapturingWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// HTTPMiddleware membuat span OpenTelemetry per request HTTP dan menyertakan header trace.
func HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		spanName := fmt.Sprintf("HTTP %s %s", r.Method, r.URL.Path)
		ctx, span := Tracer().Start(ctx, spanName,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.method", r.Method),
				attribute.String("http.route", r.URL.Path),
			),
		)
		defer span.End()

		corrID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
		if corrID == "" {
			if traceID, _ := TraceIDsFromContext(ctx); traceID != "" {
				corrID = traceID
			} else {
				corrID = uuid.NewString()
			}
		}
		ctx = WithCorrelationID(ctx, corrID)

		if traceID, _ := TraceIDsFromContext(ctx); traceID != "" {
			w.Header().Set("X-Trace-ID", traceID)
		}
		w.Header().Set("X-Correlation-ID", corrID)

		rec := &statusCapturingWriter{ResponseWriter: w, status: http.StatusOK}
		started := time.Now()
		next.ServeHTTP(rec, r.WithContext(ctx))
		dur := time.Since(started)

		span.SetAttributes(
			attribute.Int("http.status_code", rec.status),
			attribute.Float64("http.duration_ms", float64(dur.Microseconds())/1000.0),
		)
		if rec.status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, http.StatusText(rec.status))
		}
	})
}
