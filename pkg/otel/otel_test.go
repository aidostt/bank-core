package otel

import (
	"context"
	"testing"

	"github.com/aidostt/bank-core/pkg/logging"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// With no endpoint, Init installs the propagator and a no-op shutdown, so
// services run without the obs profile.
func TestInitNoEndpointInstallsPropagator(t *testing.T) {
	shutdown, err := Init(context.Background(), "test-svc", "", logging.New("otel-test"))
	if err != nil {
		t.Fatal(err)
	}
	if shutdown == nil {
		t.Fatal("nil shutdown")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("no-op shutdown: %v", err)
	}
	// Propagator must round-trip a trace context.
	if otel.GetTextMapPropagator() == nil {
		t.Fatal("propagator not installed")
	}
}

func TestInjectMap(t *testing.T) {
	// Without an active span there is nothing to inject.
	if m := InjectMap(context.Background()); m != nil {
		t.Fatalf("expected nil map without a span, got %v", m)
	}

	// With a carrier round-trip through the installed propagator, inject +
	// extract should preserve traceparent.
	otel.SetTextMapPropagator(propagation.TraceContext{})
	carrier := propagation.MapCarrier{
		"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	}
	ctx := otel.GetTextMapPropagator().Extract(context.Background(), carrier)
	out := InjectMap(ctx)
	if out["traceparent"] == "" {
		t.Fatalf("traceparent not injected: %v", out)
	}
}
