// ledger-service entrypoint. Manual DI happens here and nowhere else.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	ledgerv1 "github.com/aidostt/bank-core/gen/go/bank/ledger/v1"
	"github.com/aidostt/bank-core/pkg/grpcx"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/aidostt/bank-core/pkg/outbox"
	"github.com/aidostt/bank-core/pkg/pgtx"
	"google.golang.org/grpc"

	grpcadapter "github.com/aidostt/bank-core/services/ledger/internal/adapters/grpc"
	"github.com/aidostt/bank-core/services/ledger/internal/adapters/postgres"
	"github.com/aidostt/bank-core/services/ledger/internal/app"
	"github.com/aidostt/bank-core/services/ledger/internal/config"
	"github.com/aidostt/bank-core/services/ledger/migrations"
)

func main() {
	log := logging.New("ledger")
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

	if err := pgtx.Migrate(cfg.DBDSN, migrations.FS, "."); err != nil {
		return err
	}
	pool, err := pgtx.Connect(ctx, cfg.DBDSN, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	// Partitions for current + next month before serving (ADR-0017).
	if err := postgres.EnsurePartitions(ctx, pool, time.Now().UTC()); err != nil {
		return err
	}
	go postgres.RunPartitionMaintenance(ctx, pool, log)

	store := postgres.NewStore(pool)
	svc := app.NewService(store, cfg.HoldDefaultTTL, log)

	// Outbox relay: the only path events take to Kafka (ADR-0009).
	relay, err := outbox.NewRelay(pool, cfg.KafkaBrokers, log)
	if err != nil {
		return err
	}
	defer relay.Close()
	go relay.Run(ctx)

	// Expired-hold sweeper (ledger doc, Failure & ops).
	go app.NewSweeper(svc, 30*time.Second, log).Run(ctx)

	grpcServer := grpc.NewServer(grpcx.ServerOptions(log)...)
	ledgerv1.RegisterLedgerServiceServer(grpcServer, grpcadapter.NewServer(svc))

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	// Readiness = DB ping only: no broker dependency for serving gRPC — the
	// outbox decouples (ledger doc, Failure & ops).
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		pingCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(pingCtx); err != nil {
			http.Error(w, "db not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	httpServer := &http.Server{Addr: cfg.HTTPAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	errCh := make(chan error, 2)
	go func() {
		log.Info("grpc listening", slog.String("addr", cfg.GRPCAddr))
		errCh <- grpcServer.Serve(lis)
	}()
	go func() {
		log.Info("http listening", slog.String("addr", cfg.HTTPAddr))
		if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-errCh:
		return err
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { grpcServer.GracefulStop(); close(done) }()
	select {
	case <-done:
	case <-shutdownCtx.Done():
		grpcServer.Stop()
	}
	_ = httpServer.Shutdown(shutdownCtx)
	return nil
}
