// account-service entrypoint. Manual DI happens here and nowhere else.
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

	accountv1 "github.com/aidostt/bank-core/gen/go/bank/account/v1"
	"github.com/aidostt/bank-core/pkg/grpcx"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/aidostt/bank-core/pkg/pgtx"
	"google.golang.org/grpc"

	grpcadapter "github.com/aidostt/bank-core/services/account/internal/adapters/grpc"
	"github.com/aidostt/bank-core/services/account/internal/adapters/ledgerclient"
	"github.com/aidostt/bank-core/services/account/internal/adapters/postgres"
	"github.com/aidostt/bank-core/services/account/internal/app"
	"github.com/aidostt/bank-core/services/account/internal/config"
	"github.com/aidostt/bank-core/services/account/migrations"
)

func main() {
	log := logging.New("account")
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

	ledger, err := ledgerclient.New(cfg.LedgerAddr, log)
	if err != nil {
		return err
	}
	defer func() { _ = ledger.Close() }()

	svc := app.NewService(postgres.NewStore(pool), ledger, log)

	grpcServer := grpc.NewServer(grpcx.ServerOptions(log)...)
	accountv1.RegisterAccountServiceServer(grpcServer, grpcadapter.NewServer(svc))

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
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
