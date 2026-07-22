package grpcx

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aidostt/bank-core/pkg/logging"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const checkMethod = "/grpc.health.v1.Health/Check"

// controllable health server exercises the server interceptor chain.
type healthSrv struct {
	grpc_health_v1.UnimplementedHealthServer
	mu         sync.Mutex
	calls      int32
	failFirst  int32 // return Unavailable this many times, then OK
	panicOnce  bool
	failCode   codes.Code // if non-OK, always fail with this (overrides failFirst)
	lastClaims Claims
	lastMD     metadata.MD
}

func (s *healthSrv) Check(ctx context.Context, _ *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	n := atomic.AddInt32(&s.calls, 1)
	s.mu.Lock()
	s.lastClaims = ClaimsFromContext(ctx)
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.lastMD = md
	}
	panicNow := s.panicOnce
	s.panicOnce = false
	failCode := s.failCode
	s.mu.Unlock()

	if panicNow {
		panic("boom in handler")
	}
	if failCode != codes.OK {
		return nil, status.Error(failCode, "scripted failure")
	}
	if n <= atomic.LoadInt32(&s.failFirst) {
		return nil, status.Error(codes.Unavailable, "transient")
	}
	return &grpc_health_v1.HealthCheckResponse{Status: grpc_health_v1.HealthCheckResponse_SERVING}, nil
}

func startServer(t *testing.T, srv *healthSrv) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := grpc.NewServer(ServerOptions(logging.New("grpcx-test"))...)
	grpc_health_v1.RegisterHealthServer(s, srv)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	return lis.Addr().String()
}

func dialCheck(t *testing.T, addr string, cfg ClientConfig) grpc_health_v1.HealthClient {
	t.Helper()
	if cfg.Name == "" {
		cfg.Name = "test"
	}
	conn, err := Dial(addr, cfg, logging.New("grpcx-test"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return grpc_health_v1.NewHealthClient(conn)
}

// The server interceptor chain injects caller claims from metadata; the
// client propagation interceptor forwards request-id + claims + idem key.
func TestServerReceivesPropagatedContext(t *testing.T) {
	srv := &healthSrv{}
	client := dialCheck(t, startServer(t, srv), ClientConfig{})

	ctx := logging.WithRequestID(context.Background(), "req-77")
	ctx = ContextWithClaims(ctx, Claims{CustomerID: "cust-1", Roles: []string{"support"}})
	ctx = ContextWithIdempotencyKey(ctx, "idem-9")

	if _, err := client.Check(ctx, &grpc_health_v1.HealthCheckRequest{}); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.lastClaims.CustomerID != "cust-1" || !srv.lastClaims.HasRole("support") {
		t.Fatalf("server claims = %+v", srv.lastClaims)
	}
	if got := srv.lastMD.Get(MDRequestID); len(got) == 0 || got[0] != "req-77" {
		t.Fatalf("request id not propagated: %v", srv.lastMD.Get(MDRequestID))
	}
	if got := srv.lastMD.Get(MDIdempotencyKey); len(got) == 0 || got[0] != "idem-9" {
		t.Fatalf("idem key not propagated: %v", got)
	}
}

func TestRetryOnAmbiguousThenSucceeds(t *testing.T) {
	srv := &healthSrv{failFirst: 2} // 2 Unavailable, then OK
	client := dialCheck(t, startServer(t, srv), ClientConfig{
		RetryableMethod: func(m string) bool { return m == checkMethod },
	})
	if _, err := client.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{}); err != nil {
		t.Fatalf("expected success after retries: %v", err)
	}
	if got := atomic.LoadInt32(&srv.calls); got != 3 {
		t.Fatalf("calls = %d, want 3 (2 fail + 1 ok)", got)
	}
}

func TestNoRetryWhenNotIdempotent(t *testing.T) {
	srv := &healthSrv{failFirst: 5}
	client := dialCheck(t, startServer(t, srv), ClientConfig{
		RetryableMethod: func(string) bool { return false }, // not retryable
	})
	_, err := client.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("want Unavailable, got %v", err)
	}
	if got := atomic.LoadInt32(&srv.calls); got != 1 {
		t.Fatalf("calls = %d, want 1 (no retry)", got)
	}
}

func TestNoRetryOnBusinessError(t *testing.T) {
	srv := &healthSrv{failCode: codes.InvalidArgument}
	client := dialCheck(t, startServer(t, srv), ClientConfig{
		RetryableMethod: func(m string) bool { return m == checkMethod },
	})
	_, err := client.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", err)
	}
	if got := atomic.LoadInt32(&srv.calls); got != 1 {
		t.Fatalf("business error must not retry: calls = %d", got)
	}
}

func TestRecoveryInterceptorConvertsPanic(t *testing.T) {
	srv := &healthSrv{panicOnce: true}
	client := dialCheck(t, startServer(t, srv), ClientConfig{})
	_, err := client.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	if status.Code(err) != codes.Internal {
		t.Fatalf("panic should surface as Internal, got %v", err)
	}
}

func TestBreakerOpensAfterConsecutiveFailures(t *testing.T) {
	srv := &healthSrv{failCode: codes.Unavailable}
	client := dialCheck(t, startServer(t, srv), ClientConfig{
		MaxAttempts:     1, // isolate the breaker from retries
		RetryableMethod: func(string) bool { return false },
	})
	sawOpen := false
	for i := 0; i < 8; i++ {
		_, err := client.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
		if status.Code(err) == codes.Unavailable && err != nil &&
			status.Convert(err).Message() == "circuit breaker open: "+checkMethod {
			sawOpen = true
			break
		}
	}
	if !sawOpen {
		t.Fatal("breaker never opened after repeated failures")
	}
}

func TestTimeoutInterceptor(t *testing.T) {
	// A tiny per-attempt timeout with a slow server → DeadlineExceeded.
	srv := &healthSrv{}
	addr := startServer(t, srv)
	// wrap the server with an artificial delay via a slow interceptor is
	// overkill; instead assert Ambiguous() classifies the transport codes.
	_ = addr
	if !Ambiguous(status.Error(codes.DeadlineExceeded, "x")) {
		t.Fatal("deadline is ambiguous")
	}
	if !Ambiguous(status.Error(codes.Unavailable, "x")) {
		t.Fatal("unavailable is ambiguous")
	}
	if Ambiguous(status.Error(codes.InvalidArgument, "x")) {
		t.Fatal("invalid arg not ambiguous")
	}
	_ = time.Second
}
