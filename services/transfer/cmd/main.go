// transfer-service entrypoint. Manual DI happens here and nowhere else.
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

	transferv1 "github.com/aidostt/bank-core/gen/go/bank/transfer/v1"
	"github.com/aidostt/bank-core/pkg/grpcx"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/aidostt/bank-core/pkg/metrics"
	otelx "github.com/aidostt/bank-core/pkg/otel"
	"github.com/aidostt/bank-core/pkg/outbox"
	"github.com/aidostt/bank-core/pkg/pgtx"
	"google.golang.org/grpc"

	"github.com/aidostt/bank-core/services/transfer/internal/adapters/accountclient"
	grpcadapter "github.com/aidostt/bank-core/services/transfer/internal/adapters/grpc"
	"github.com/aidostt/bank-core/services/transfer/internal/adapters/ledgerclient"
	"github.com/aidostt/bank-core/services/transfer/internal/adapters/postgres"
	"github.com/aidostt/bank-core/services/transfer/internal/app"
	"github.com/aidostt/bank-core/services/transfer/internal/config"
	"github.com/aidostt/bank-core/services/transfer/migrations"
)

func main() {
	log := logging.New("transfer")
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

	otelShutdown, err := otelx.Init(ctx, "transfer", cfg.OTLPEndpoint, log)
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

	ledger, err := ledgerclient.New(cfg.LedgerAddr, log)
	if err != nil {
		return err
	}
	defer func() { _ = ledger.Close() }()
	account, err := accountclient.New(cfg.AccountAddr, log)
	if err != nil {
		return err
	}
	defer func() { _ = account.Close() }()

	store := postgres.NewStore(pool)
	svc := app.NewService(store, ledger, account, log)

	// Outbox relay (ADR-0009).
	relay, err := outbox.NewRelay(pool, cfg.KafkaBrokers, log)
	if err != nil {
		return err
	}
	defer relay.Close()
	go relay.Run(ctx)

	// Recovery worker for stuck sagas (ADR-0010).
	go app.NewRecovery(svc, cfg.RecoveryInterval, cfg.RecoveryStaleAfter, log).Run(ctx)

	// Idempotency-key TTL cleanup (ADR-0012).
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				svc.CleanupIdempotencyKeys(ctx)
			}
		}
	}()

	grpcServer := grpc.NewServer(grpcx.ServerOptions(log)...)
	transferv1.RegisterTransferServiceServer(grpcServer, grpcadapter.NewServer(svc))

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return err
	}

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
