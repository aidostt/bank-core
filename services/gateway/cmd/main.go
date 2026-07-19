// gateway entrypoint. Manual DI happens here and nowhere else.
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

	"github.com/aidostt/bank-core/pkg/logging"

	"github.com/aidostt/bank-core/services/gateway/internal/adapters/grpcclients"
	httpadapter "github.com/aidostt/bank-core/services/gateway/internal/adapters/http"
	redisadapter "github.com/aidostt/bank-core/services/gateway/internal/adapters/redis"
	"github.com/aidostt/bank-core/services/gateway/internal/app"
	"github.com/aidostt/bank-core/services/gateway/internal/config"
)

func main() {
	log := logging.New("gateway")
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

	jwks := app.NewJWKSCache(cfg.JWKSURL, cfg.JWTIssuer)
	if err := jwks.WarmUp(60 * time.Second); err != nil {
		return err
	}

	clients, err := grpcclients.Dial(cfg.IdentityAddr, cfg.AccountAddr, cfg.TransferAddr, log)
	if err != nil {
		return err
	}
	defer clients.Close()

	limiter := redisadapter.NewLimiter(cfg.RedisAddr)
	defer func() { _ = limiter.Close() }()

	server := httpadapter.NewServer(cfg, jwks, clients, limiter, log)
	httpServer := server.HTTPServer()

	errCh := make(chan error, 1)
	go func() {
		log.Info("gateway listening", slog.String("addr", cfg.HTTPAddr))
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
	return httpServer.Shutdown(shutdownCtx)
}
