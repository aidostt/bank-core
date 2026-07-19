package grpcx

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/aidostt/bank-core/pkg/logging"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// ServerOptions returns the standard interceptor chain for every gRPC server
// (CLAUDE.md §3): recovery → logging → auth-claims. The otel link in the
// chain lands in M2 together with pkg/otel.
func ServerOptions(log *slog.Logger) []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(
			recoveryInterceptor(log),
			loggingInterceptor(log),
			claimsInterceptor(),
		),
	}
}

func recoveryInterceptor(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				log.ErrorContext(ctx, "panic recovered",
					slog.Any("panic", r),
					slog.String("method", info.FullMethod),
					slog.String("stack", string(debug.Stack())))
				err = status.Error(codes.Internal, "internal error")
			}
		}()
		return handler(ctx, req)
	}
}

func loggingInterceptor(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			ctx = logging.WithRequestID(ctx, mdFirst(md, MDRequestID))
		}
		start := time.Now()
		resp, err := handler(ctx, req)
		code := status.Code(err)
		lvl := slog.LevelInfo
		if code == codes.Internal || code == codes.Unknown {
			lvl = slog.LevelError
		}
		attrs := []slog.Attr{
			slog.String("method", info.FullMethod),
			slog.String("code", code.String()),
			slog.Duration("duration", time.Since(start)),
		}
		if err != nil {
			// Full error server-side, keyed by request.id (ADR-0018); the
			// client only ever sees the sanitized status.
			attrs = append(attrs, slog.String("error", err.Error()))
		}
		log.LogAttrs(ctx, lvl, "grpc request", attrs...)
		return resp, err
	}
}

func claimsInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			claims := Claims{
				CustomerID: mdFirst(md, MDCustomerID),
				Roles:      parseRoles(mdFirst(md, MDRoles)),
			}
			ctx = ContextWithClaims(ctx, claims)
			if k := mdFirst(md, MDIdempotencyKey); k != "" {
				ctx = ContextWithIdempotencyKey(ctx, k)
			}
		}
		return handler(ctx, req)
	}
}
