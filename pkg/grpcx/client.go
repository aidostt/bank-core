package grpcx

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/sony/gobreaker/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// ClientConfig is the standard resilience policy for every internal gRPC
// client (CLAUDE.md §3): per-attempt timeout → retry (idempotent methods
// only) → circuit breaker.
type ClientConfig struct {
	// Name identifies the breaker in logs, e.g. "transfer→ledger".
	Name string
	// Timeout per attempt; 0 means 2s (default policy).
	Timeout time.Duration
	// MaxAttempts including the first call; 0 means 3.
	MaxAttempts int
	// RetryableMethod reports whether a full method name (e.g.
	// "/bank.ledger.v1.LedgerService/PlaceHold") is idempotent and may be
	// retried on UNAVAILABLE/DEADLINE_EXCEEDED. Nil = no retries.
	RetryableMethod func(fullMethod string) bool
}

// Dial creates a client connection with the standard chain:
// retry (outermost) → breaker → timeout → propagation.
func Dial(target string, cfg ClientConfig, log *slog.Logger) (*grpc.ClientConn, error) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 2 * time.Second
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 3
	}
	breaker := gobreaker.NewCircuitBreaker[any](gobreaker.Settings{
		Name:    cfg.Name,
		Timeout: 10 * time.Second, // half-open probe delay
		ReadyToTrip: func(c gobreaker.Counts) bool {
			return c.ConsecutiveFailures >= 5
		},
		IsSuccessful: func(err error) bool {
			// Business failures are not breaker failures — only transport
			// trouble should open the circuit.
			return err == nil || !Ambiguous(err)
		},
		OnStateChange: func(name string, from, to gobreaker.State) {
			log.Warn("circuit breaker state change",
				slog.String("breaker", name),
				slog.String("from", from.String()),
				slog.String("to", to.String()))
		},
	})
	return grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(
			retryInterceptor(cfg, log),
			breakerInterceptor(breaker),
			timeoutInterceptor(cfg.Timeout),
			propagationInterceptor(),
		),
	)
}

// Ambiguous reports whether the outcome of a call is unknown — the request
// may or may not have been executed by the server.
func Ambiguous(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded:
		return true
	default:
		return false
	}
}

func retryInterceptor(cfg ClientConfig, log *slog.Logger) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		attempts := 1
		if cfg.RetryableMethod != nil && cfg.RetryableMethod(method) {
			attempts = cfg.MaxAttempts
		}
		var err error
		for i := 0; i < attempts; i++ {
			if i > 0 {
				// Exponential backoff with jitter: 100ms, 200ms, ... ±50%.
				base := 100 * time.Millisecond << (i - 1)
				jitter := time.Duration(rand.Int64N(int64(base))) / 2 // #nosec G404 -- retry jitter, not security-sensitive
				select {
				case <-time.After(base + jitter):
				case <-ctx.Done():
					return err
				}
				log.DebugContext(ctx, "retrying grpc call",
					slog.String("method", method), slog.Int("attempt", i+1))
			}
			err = invoker(ctx, method, req, reply, cc, opts...)
			if err == nil || !Ambiguous(err) {
				return err
			}
		}
		return err
	}
}

func breakerInterceptor(cb *gobreaker.CircuitBreaker[any]) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		_, err := cb.Execute(func() (any, error) {
			return nil, invoker(ctx, method, req, reply, cc, opts...)
		})
		if err == gobreaker.ErrOpenState || err == gobreaker.ErrTooManyRequests {
			return status.Error(codes.Unavailable, "circuit breaker open: "+method)
		}
		return err
	}
}

func timeoutInterceptor(d time.Duration) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		ctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// propagationInterceptor forwards correlation id, caller claims and the
// idempotency key to the outgoing metadata (architecture §6).
func propagationInterceptor() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		pairs := make([]string, 0, 8)
		if id := logging.RequestID(ctx); id != "" {
			pairs = append(pairs, MDRequestID, id)
		}
		if c := ClaimsFromContext(ctx); c.CustomerID != "" {
			pairs = append(pairs, MDCustomerID, c.CustomerID)
			if len(c.Roles) > 0 {
				pairs = append(pairs, MDRoles, joinRoles(c.Roles))
			}
		}
		if k := IdempotencyKeyFromContext(ctx); k != "" {
			pairs = append(pairs, MDIdempotencyKey, k)
		}
		if len(pairs) > 0 {
			ctx = metadata.AppendToOutgoingContext(ctx, pairs...)
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func joinRoles(roles []string) string {
	out := ""
	for i, r := range roles {
		if i > 0 {
			out += ","
		}
		out += r
	}
	return out
}
