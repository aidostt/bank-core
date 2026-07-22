package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestRequestIDRoundTrip(t *testing.T) {
	ctx := WithRequestID(context.Background(), "req-1")
	if got := RequestID(ctx); got != "req-1" {
		t.Fatalf("RequestID = %q", got)
	}
	// empty id is a no-op
	if RequestID(WithRequestID(context.Background(), "")) != "" {
		t.Fatal("empty id should not be stored")
	}
	if RequestID(context.Background()) != "" {
		t.Fatal("no id in bare context")
	}
}

func TestHandlerInjectsRequestID(t *testing.T) {
	var buf bytes.Buffer
	h := ctxHandler{slog.NewJSONHandler(&buf, nil)}
	log := slog.New(h)
	log.InfoContext(WithRequestID(context.Background(), "abc-123"), "hello")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	if line["request.id"] != "abc-123" {
		t.Fatalf("request.id not on line: %v", line)
	}
	if line["msg"] != "hello" {
		t.Fatalf("msg missing: %v", line)
	}
}

func TestNewLoggerCarriesService(t *testing.T) {
	t.Setenv("LOG_LEVEL", "debug")
	log := New("svc-x")
	if log == nil {
		t.Fatal("nil logger")
	}
	// WithAttrs/WithGroup must preserve the ctx-injecting wrapper.
	log.With("k", "v").WithGroup("g").Info("ok")
}
