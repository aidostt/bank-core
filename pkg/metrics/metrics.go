// Package metrics: prometheus registry + RED instrumentation shared by all
// services (ADR-0013, architecture §8). Business metrics are registered by
// their owning service through the same registry.
package metrics

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// Registry is the process-wide registry; every service exposes it on
// /metrics via Handler().
var Registry = func() *prometheus.Registry {
	r := prometheus.NewRegistry()
	r.MustRegister(collectors.NewGoCollector())
	r.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return r
}()

func Handler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{})
}

// --- RED: gRPC servers ---

var (
	grpcRequests = promauto.With(Registry).NewCounterVec(prometheus.CounterOpts{
		Name: "grpc_requests_total",
		Help: "gRPC requests by full method and status code.",
	}, []string{"method", "code"})
	grpcDuration = promauto.With(Registry).NewHistogramVec(prometheus.HistogramOpts{
		Name:    "grpc_request_duration_seconds",
		Help:    "gRPC request duration.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method"})
)

// GRPCServerInterceptor records RED metrics for every unary call.
func GRPCServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		grpcRequests.WithLabelValues(info.FullMethod, status.Code(err).String()).Inc()
		grpcDuration.WithLabelValues(info.FullMethod).Observe(time.Since(start).Seconds())
		return resp, err
	}
}

// --- RED: HTTP (gateway) ---

var (
	httpRequests = promauto.With(Registry).NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "HTTP requests by route and status.",
	}, []string{"method", "route", "status"})
	httpDuration = promauto.With(Registry).NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request duration by route.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route"})
)

// ObserveHTTP is called by the gateway middleware per request.
func ObserveHTTP(method, route string, statusCode int, duration time.Duration) {
	httpRequests.WithLabelValues(method, route, strconv.Itoa(statusCode)).Inc()
	httpDuration.WithLabelValues(method, route).Observe(duration.Seconds())
}

// --- business metrics (architecture §8) ---

var (
	TransfersTotal = promauto.With(Registry).NewCounterVec(prometheus.CounterOpts{
		Name: "transfers_total",
		Help: "Transfers reaching a terminal state.",
	}, []string{"state"})
	LedgerPostingsTotal = promauto.With(Registry).NewCounter(prometheus.CounterOpts{
		Name: "ledger_postings_total",
		Help: "Journal postings appended.",
	})
	FraudAlertsTotal = promauto.With(Registry).NewCounterVec(prometheus.CounterOpts{
		Name: "fraud_alerts_total",
		Help: "Fraud alerts raised.",
	}, []string{"severity"})
	OutboxLag = promauto.With(Registry).NewGauge(prometheus.GaugeOpts{
		Name: "outbox_lag",
		Help: "Unsent outbox rows.",
	})
	ConsumerLag = promauto.With(Registry).NewGaugeVec(prometheus.GaugeOpts{
		Name: "consumer_lag",
		Help: "Kafka consumer lag per topic/partition.",
	}, []string{"topic", "partition"})
	NotificationsTotal = promauto.With(Registry).NewCounterVec(prometheus.CounterOpts{
		Name: "notifications_total",
		Help: "Notifications rendered by status.",
	}, []string{"status"})
)

// ObserveConsumerLag adapts ConsumerLag to the pkg/kafka LagObserver port.
func ObserveConsumerLag(topic string, partition int32, lag int64) {
	if lag < 0 {
		lag = 0
	}
	ConsumerLag.WithLabelValues(topic, strconv.Itoa(int(partition))).Set(float64(lag))
}
