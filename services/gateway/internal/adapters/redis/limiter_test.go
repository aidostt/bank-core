package redis

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

func TestFixedWindowLimiter(t *testing.T) {
	mr := miniredis.RunT(t)
	l := NewLimiter(mr.Addr())
	defer func() { _ = l.Close() }()
	ctx := context.Background()

	// First `limit` calls in the window are allowed, the next is not.
	for i := 0; i < 3; i++ {
		ok, err := l.Allow(ctx, "user-1", "GET /v1/rates", 3)
		if err != nil || !ok {
			t.Fatalf("call %d allowed=%v err=%v", i, ok, err)
		}
	}
	ok, err := l.Allow(ctx, "user-1", "GET /v1/rates", 3)
	if err != nil || ok {
		t.Fatalf("4th call should be limited: allowed=%v err=%v", ok, err)
	}

	// A different subject has an independent window.
	if ok, err := l.Allow(ctx, "user-2", "GET /v1/rates", 3); err != nil || !ok {
		t.Fatalf("other subject: allowed=%v err=%v", ok, err)
	}

	// Advancing past the 1s window resets the counter.
	mr.FastForward(2e9) // 2s in nanoseconds
	if ok, err := l.Allow(ctx, "user-1", "GET /v1/rates", 3); err != nil || !ok {
		t.Fatalf("after window roll: allowed=%v err=%v", ok, err)
	}
}

func TestLimiterErrorsWhenRedisDown(t *testing.T) {
	l := NewLimiter("127.0.0.1:1") // nothing listening
	defer func() { _ = l.Close() }()
	if _, err := l.Allow(context.Background(), "s", "r", 5); err == nil {
		t.Fatal("want error when redis unreachable (caller fails open on it)")
	}
}
