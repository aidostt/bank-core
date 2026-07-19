// Package redis: fixed-window rate limiter (api-gateway doc). Correctness
// of money flows never depends on Redis (ADR-0012) — this is throttling
// only, and it fails open.
package redis

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

type Limiter struct {
	client *goredis.Client
}

func NewLimiter(addr string) *Limiter {
	return &Limiter{client: goredis.NewClient(&goredis.Options{
		Addr:        addr,
		DialTimeout: 500 * time.Millisecond,
		ReadTimeout: 500 * time.Millisecond,
	})}
}

func (l *Limiter) Close() error { return l.client.Close() }

// Allow increments the current 1-second window counter for subject+route.
func (l *Limiter) Allow(ctx context.Context, subject, route string, limit int) (bool, error) {
	window := time.Now().Unix()
	key := fmt.Sprintf("rl:%s:%s:%d", subject, route, window)
	pipe := l.client.TxPipeline()
	incr := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, 2*time.Second)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, err
	}
	return incr.Val() <= int64(limit), nil
}
