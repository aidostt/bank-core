// antifraud-service — M1 stub: health endpoints only, so the compose core
// profile builds and runs all 7 services. The consumer runtime, rules and
// alerts land in M2 (prompts/M2.md, docs/services/anti-fraud-service.md).
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

	"github.com/aidostt/bank-core/pkg/config"
	"github.com/aidostt/bank-core/pkg/logging"
)

func main() {
	log := logging.New("antifraud")
	l := config.New()
	addr := l.StringDefault("ANTIFRAUD_HTTP_ADDR", ":8085")
	if err := l.Err(); err != nil {
		log.Error("fatal", slog.String("error", err.Error()))
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	go func() {
		log.Info("http listening (M2 will add the scoring consumer)", slog.String("addr", addr))
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", slog.String("error", err.Error()))
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
}
