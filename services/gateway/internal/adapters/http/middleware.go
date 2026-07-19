package http

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aidostt/bank-core/pkg/apperr"
	"github.com/aidostt/bank-core/pkg/grpcx"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/aidostt/bank-core/services/gateway/internal/app"
)

const maxBodyBytes = 1 << 20 // 1 MiB

// requestID: correlation id for every hop (architecture §6).
func requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-ID")
		if id == "" {
			v7, err := uuid.NewV7()
			if err == nil {
				id = v7.String()
			}
		}
		c.Header("X-Request-ID", id)
		c.Request = c.Request.WithContext(logging.WithRequestID(c.Request.Context(), id))
		c.Next()
	}
}

func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		c.Next()
	}
}

func cors() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, X-Request-ID")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func bodyLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)
		c.Next()
	}
}

func loggingMiddleware(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		lvl := slog.LevelInfo
		if c.Writer.Status() >= 500 {
			lvl = slog.LevelError
		}
		log.LogAttrs(c.Request.Context(), lvl, "http request",
			slog.String("method", c.Request.Method),
			slog.String("path", c.FullPath()),
			slog.Int("status", c.Writer.Status()),
			slog.Duration("duration", time.Since(start)))
	}
}

// timeoutFor applies the per-class deadline (reads 1s, transfers 5s).
func timeoutFor(class app.RouteClass) gin.HandlerFunc {
	d := time.Second
	switch class {
	case app.ClassTransfer:
		d = 5 * time.Second
	case app.ClassPublic, app.ClassWrite:
		d = 3 * time.Second
	}
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), d)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// authMiddleware validates the bearer token and stores claims in the
// request context for gRPC metadata propagation (grpcx).
func (s *Server) authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := c.GetHeader("Authorization")
		if !strings.HasPrefix(raw, "Bearer ") {
			writeProblem(c, apperr.New(apperr.CodeUnauthenticated, "missing bearer token"))
			return
		}
		claims, err := s.jwks.Validate(strings.TrimPrefix(raw, "Bearer "))
		if err != nil {
			writeProblem(c, apperr.New(apperr.CodeUnauthenticated, "invalid or expired token"))
			return
		}
		ctx := grpcx.ContextWithClaims(c.Request.Context(), grpcx.Claims{
			CustomerID: claims.UserID,
			Roles:      claims.Roles,
		})
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func (s *Server) rbacMiddleware(route app.Route) gin.HandlerFunc {
	return func(c *gin.Context) {
		claims := grpcx.ClaimsFromContext(c.Request.Context())
		if !app.Allowed(route, claims.Roles) {
			writeProblem(c, apperr.New(apperr.CodeForbidden, "role not allowed for this route"))
			return
		}
		c.Next()
	}
}

// idempotencyKeyMiddleware enforces the mandatory header (ADR-0012).
func idempotencyKeyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.GetHeader("Idempotency-Key")
		if key == "" {
			writeProblem(c, apperr.New(apperr.CodeIdempotencyKeyRequired, "Idempotency-Key header is required"))
			return
		}
		c.Request = c.Request.WithContext(grpcx.ContextWithIdempotencyKey(c.Request.Context(), key))
		c.Next()
	}
}

// rateLimitMiddleware: Redis fixed window per subject+route; fails open
// when Redis is down (architecture §7).
func (s *Server) rateLimitMiddleware(route app.Route) gin.HandlerFunc {
	limit := s.cfg.RateLimitReads
	if route.Class == app.ClassTransfer {
		limit = s.cfg.RateLimitWrites
	}
	routeID := route.Method + " " + route.Path
	return func(c *gin.Context) {
		subject := grpcx.ClaimsFromContext(c.Request.Context()).CustomerID
		if subject == "" {
			subject = c.ClientIP()
		}
		allowed, err := s.limiter.Allow(c.Request.Context(), subject, routeID, limit)
		if err != nil {
			s.log.WarnContext(c.Request.Context(), "rate limiter unavailable — failing open",
				slog.String("error", err.Error()))
			c.Next()
			return
		}
		if !allowed {
			writeProblem(c, apperr.New(apperr.CodeRateLimited, "rate limit exceeded"))
			return
		}
		c.Next()
	}
}
