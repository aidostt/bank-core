// antifraud-service entrypoint (M2): async rule-based scoring off
// transfers.status, alerts through the outbox. Manual DI here only.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	kafkart "github.com/aidostt/bank-core/pkg/kafka"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/aidostt/bank-core/pkg/metrics"
	otelx "github.com/aidostt/bank-core/pkg/otel"
	"github.com/aidostt/bank-core/pkg/outbox"
	"github.com/aidostt/bank-core/pkg/pgtx"

	"github.com/aidostt/bank-core/services/antifraud/internal/app"
	"github.com/aidostt/bank-core/services/antifraud/internal/config"
	"github.com/aidostt/bank-core/services/antifraud/migrations"
)

func main() {
	log := logging.New("antifraud")
	if err := run(log); err != nil {
		log.Error("fatal", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	otelShutdown, err := otelx.Init(ctx, "antifraud", cfg.OTLPEndpoint, log)
	if err != nil {
		return err
	}
	defer func() { _ = otelShutdown(context.Background()) }()

	if err := pgtx.Migrate(cfg.DBDSN, migrations.FS, "."); err != nil {
		return err
	}
	pool, err := pgtx.Connect(ctx, cfg.DBDSN, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	scorer := app.NewScorer(log)
	consumer, err := kafkart.NewConsumer(kafkart.Config{
		Brokers: cfg.KafkaBrokers,
		Group:   app.Group,
		Topics:  []string{app.TopicTransfersState},
	}, pool, scorer.Handle, metrics.ObserveConsumerLag, log)
	if err != nil {
		return err
	}
	defer consumer.Close()
	go consumer.Run(ctx)

	relay, err := outbox.NewRelay(pool, cfg.KafkaBrokers, log)
	if err != nil {
		return err
	}
	defer relay.Close()
	go relay.Run(ctx)

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		pingCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(pingCtx); err != nil {
			http.Error(w, "db not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	server := &http.Server{Addr: cfg.HTTPAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	go func() {
		log.Info("http listening", slog.String("addr", cfg.HTTPAddr))
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", slog.String("error", err.Error()))
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	return nil
}
