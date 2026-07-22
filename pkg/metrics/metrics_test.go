package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestBusinessMetricsAndHandler(t *testing.T) {
	TransfersTotal.WithLabelValues("COMPLETED").Inc()
	LedgerPostingsTotal.Add(3)
	FraudAlertsTotal.WithLabelValues("HIGH").Inc()
	OutboxLag.Set(5)
	ConsumerLag.WithLabelValues("ledger.transactions", "0").Set(2)
	NotificationsTotal.WithLabelValues("sent").Inc()
	ObserveHTTP("GET", "/v1/accounts", 200, 12*time.Millisecond)
	ObserveConsumerLag("ledger.transactions", 1, 7)

	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"transfers_total", "ledger_postings_total", "fraud_alerts_total",
		"outbox_lag", "consumer_lag", "notifications_total", "http_request_duration_seconds",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metric %q missing from /metrics output", want)
		}
	}
}

func TestGRPCServerInterceptorObserves(t *testing.T) {
	interceptor := GRPCServerInterceptor()
	info := &grpc.UnaryServerInfo{FullMethod: "/bank.ledger.v1.LedgerService/PostTransaction"}

	// success path
	if _, err := interceptor(context.Background(), nil, info,
		func(context.Context, any) (any, error) { return "ok", nil }); err != nil {
		t.Fatal(err)
	}
	// error path (code recorded)
	_, err := interceptor(context.Background(), nil, info,
		func(context.Context, any) (any, error) { return nil, status.Error(codes.FailedPrecondition, "x") })
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("interceptor should pass the error through: %v", err)
	}

	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "grpc_request_duration_seconds") {
		t.Fatal("grpc RED histogram missing")
	}
}
