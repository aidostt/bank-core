// Package http is the gateway's REST edge: thin handlers, REST↔gRPC
// translation, zero business logic (api-gateway doc).
package http

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/aidostt/bank-core/pkg/metrics"
	"github.com/gin-gonic/gin"

	"github.com/aidostt/bank-core/services/gateway/api"
	"github.com/aidostt/bank-core/services/gateway/internal/adapters/grpcclients"
	"github.com/aidostt/bank-core/services/gateway/internal/app"
	"github.com/aidostt/bank-core/services/gateway/internal/config"
)

type Server struct {
	cfg     config.Config
	jwks    TokenValidator
	clients *grpcclients.Clients
	limiter Limiter
	log     *slog.Logger
}

// TokenValidator is implemented by app.JWKSCache.
type TokenValidator interface {
	Validate(raw string) (app.TokenClaims, error)
}

// Limiter is the rate-limit port (implemented by adapters/redis.Limiter).
type Limiter interface {
	Allow(ctx context.Context, subject, route string, limit int) (bool, error)
}

func NewServer(cfg config.Config, jwks TokenValidator, clients *grpcclients.Clients, limiter Limiter, log *slog.Logger) *Server {
	return &Server{cfg: cfg, jwks: jwks, clients: clients, limiter: limiter, log: log}
}

// Router assembles middleware and the RBAC table into a Gin engine.
func (s *Server) Router() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(
		requestIDMiddleware(),
		ginRecovery(s.log),
		tracingMiddleware(),
		metricsMiddleware(),
		loggingMiddleware(s.log),
		securityHeaders(),
		cors(),
		bodyLimit(),
	)

	r.GET("/healthz", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/readyz", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/metrics", gin.WrapH(metrics.Handler()))
	r.GET("/v1/openapi.yaml", func(c *gin.Context) {
		c.Data(http.StatusOK, "application/yaml", api.OpenAPISpec)
	})

	handlers := s.handlerTable()
	for _, route := range app.Table() {
		chain := []gin.HandlerFunc{timeoutFor(route.Class)}
		if len(route.Roles) > 0 {
			chain = append(chain, s.authMiddleware(), s.rbacMiddleware(route))
		}
		chain = append(chain, s.rateLimitMiddleware(route))
		if route.Idempotency {
			chain = append(chain, idempotencyKeyMiddleware())
		}
		h, ok := handlers[route.Method+" "+route.Path]
		if !ok {
			panic("no handler registered for route " + route.Method + " " + route.Path)
		}
		chain = append(chain, h)
		r.Handle(route.Method, route.Path, chain...)
	}
	return r
}

func (s *Server) handlerTable() map[string]gin.HandlerFunc {
	return map[string]gin.HandlerFunc{
		"POST /v1/auth/register": s.handleRegister,
		"POST /v1/auth/login":    s.handleLogin,
		"POST /v1/auth/refresh":  s.handleRefresh,
		"POST /v1/auth/logout":   s.handleLogout,

		"GET /v1/customers/me": s.handleGetMe,

		"POST /v1/accounts":                 s.handleOpenAccount,
		"GET /v1/accounts":                  s.handleListAccounts,
		"GET /v1/accounts/:id/transactions": s.handleListTransactions,

		"POST /v1/topups":       s.handleTopup,
		"POST /v1/transfers":    s.handleCreateTransfer,
		"GET /v1/transfers/:id": s.handleGetTransfer,
		"GET /v1/transfers":     s.handleListTransfers,
		"GET /v1/rates":         s.handleGetRates,

		"GET /v1/admin/customers/:id/accounts": s.handleAdminListAccounts,
		"POST /v1/admin/accounts/:id/freeze":   s.handleFreeze,
		"POST /v1/admin/accounts/:id/unfreeze": s.handleUnfreeze,
	}
}

func ginRecovery(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				log.ErrorContext(c.Request.Context(), "panic recovered", slog.Any("panic", rec))
				c.AbortWithStatusJSON(http.StatusInternalServerError, Problem{
					Type: "https://bank-core.dev/errors/internal", Title: "Internal error",
					Status: http.StatusInternalServerError, Code: "INTERNAL",
				})
			}
		}()
		c.Next()
	}
}

// HTTPServer wraps the router in a production-shaped http.Server.
func (s *Server) HTTPServer() *http.Server {
	return &http.Server{
		Addr:              s.cfg.HTTPAddr,
		Handler:           s.Router(),
		ReadHeaderTimeout: 5 * time.Second,
	}
}
