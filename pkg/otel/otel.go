// Package otel centralizes tracing setup (ADR-0013): OTLP/gRPC export to
// Jaeger, W3C propagation across HTTP, gRPC and Kafka (via the event
// envelope's trace_context). When the collector endpoint is absent the
// exporter fails quietly — the core profile runs without the obs profile.
package otel

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Init configures the global tracer provider and propagator. endpoint is
// host:port of an OTLP/gRPC collector (jaeger:4317 in compose); empty
// disables tracing (no-op provider) — config stays env-only (project
// conventions §8). Returns a shutdown func for graceful flush.
func Init(ctx context.Context, service, endpoint string, log *slog.Logger) (func(context.Context) error, error) {
	// Propagator is always installed: services forward trace context even
	// when they do not export spans themselves.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))

	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}

	// Collector may be absent (core profile without obs) — log once, softly.
	var once sync.Once
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		once.Do(func() {
			log.Debug("otel export unavailable (obs profile down?)", slog.String("error", err.Error()))
		})
	}))

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithTimeout(3*time.Second),
	)
	if err != nil {
		return nil, err
	}
	// Schemaless: merging with resource.Default() trips "conflicting Schema
	// URL" whenever the SDK and the imported semconv drift apart.
	res := resource.NewSchemaless(semconv.ServiceName(service))
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}

// InjectMap captures the current span context as a W3C map — stored into
// the event envelope by pkg/outbox so consumers continue the trace.
func InjectMap(ctx context.Context) map[string]string {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	if len(carrier) == 0 {
		return nil
	}
	return carrier
}
