// identity-service entrypoint. Manual DI happens here and nowhere else
// (project conventions §2).
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

	identityv1 "github.com/aidostt/bank-core/gen/go/bank/identity/v1"
	"github.com/aidostt/bank-core/pkg/grpcx"
	"github.com/aidostt/bank-core/pkg/logging"
	otelx "github.com/aidostt/bank-core/pkg/otel"
	"github.com/aidostt/bank-core/pkg/outbox"
	"github.com/aidostt/bank-core/pkg/pgtx"
	"google.golang.org/grpc"

	grpcadapter "github.com/aidostt/bank-core/services/identity/internal/adapters/grpc"
	httpadapter "github.com/aidostt/bank-core/services/identity/internal/adapters/http"
	"github.com/aidostt/bank-core/services/identity/internal/adapters/keys"
	"github.com/aidostt/bank-core/services/identity/internal/adapters/postgres"
	"github.com/aidostt/bank-core/services/identity/internal/app"
	"github.com/aidostt/bank-core/services/identity/internal/config"
	"github.com/aidostt/bank-core/services/identity/migrations"
)

func main() {
	log := logging.New("identity")
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

	otelShutdown, err := otelx.Init(ctx, "identity", cfg.OTLPEndpoint, log)
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

	signer, err := keys.Load(cfg.KeysDir, cfg.JWTIssuer)
	if err != nil {
		return err
	}
	store := postgres.NewStore(pool)
	svc := app.NewService(store, signer, cfg.AccessTokenTTL, cfg.RefreshTokenTTL, log)

	// Outbox relay for customers.registered (M2).
	relay, err := outbox.NewRelay(pool, cfg.KafkaBrokers, log)
	if err != nil {
		return err
	}
	defer relay.Close()
	go relay.Run(ctx)

	grpcServer := grpc.NewServer(grpcx.ServerOptions(log)...)
	identityv1.RegisterIdentityServiceServer(grpcServer, grpcadapter.NewServer(svc))

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return err
	}
	httpServer := httpadapter.NewServer(cfg.HTTPAddr, signer.JWKS(), pool)

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

	// Graceful shutdown: stop intake, drain in-flight ≤10s (architecture §7).
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
