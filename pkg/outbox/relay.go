package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aidostt/bank-core/pkg/metrics"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"
)

const (
	relayBatch   = 100
	relayTick    = 200 * time.Millisecond
	lagEveryTick = 25 // outbox_lag gauge refresh ≈ every 5s
)

// Relay polls the outbox table and publishes pending rows in insertion
// order (per-key ordering is preserved by Kafka partitioning + the
// idempotent producer). Rows are marked sent only after the broker acks —
// redelivery is possible, consumers deduplicate (ADR-0009).
type Relay struct {
	pool   *pgxpool.Pool
	client *kgo.Client
	log    *slog.Logger
}

func NewRelay(pool *pgxpool.Pool, brokers []string, log *slog.Logger) (*Relay, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ProducerBatchCompression(kgo.SnappyCompression()),
	)
	if err != nil {
		return nil, fmt.Errorf("kafka client: %w", err)
	}
	return &Relay{pool: pool, client: client, log: log}, nil
}

func (r *Relay) Close() { r.client.Close() }

// Run blocks until ctx is done. The broker being down only delays delivery:
// the outbox accumulates and the system stays available for writes
// (architecture §7).
func (r *Relay) Run(ctx context.Context) {
	ticker := time.NewTicker(relayTick)
	defer ticker.Stop()
	tick := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for {
				n, err := r.relayOnce(ctx)
				if err != nil {
					if ctx.Err() == nil {
						r.log.Warn("outbox relay attempt failed", slog.String("error", err.Error()))
					}
					break
				}
				if n < relayBatch {
					break
				}
			}
			if tick++; tick%lagEveryTick == 0 {
				r.observeLag(ctx)
			}
		}
	}
}

// observeLag refreshes the outbox_lag gauge (architecture §8).
func (r *Relay) observeLag(ctx context.Context) {
	var unsent int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE sent_at IS NULL`).Scan(&unsent); err == nil {
		metrics.OutboxLag.Set(float64(unsent))
	}
}

func (r *Relay) relayOnce(ctx context.Context) (int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT id, topic, key, payload FROM outbox
		WHERE sent_at IS NULL
		ORDER BY created_at, id
		LIMIT $1
		FOR UPDATE SKIP LOCKED`, relayBatch)
	if err != nil {
		return 0, err
	}
	var ids []string
	var records []*kgo.Record
	for rows.Next() {
		var id, topic, key string
		var payload []byte
		if err := rows.Scan(&id, &topic, &key, &payload); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
		records = append(records, &kgo.Record{Topic: topic, Key: []byte(key), Value: payload})
	}
	rows.Close()
	if rows.Err() != nil {
		return 0, rows.Err()
	}
	if len(records) == 0 {
		return 0, tx.Commit(ctx)
	}

	produceCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := r.client.ProduceSync(produceCtx, records...).FirstErr(); err != nil {
		return 0, fmt.Errorf("produce: %w", err)
	}

	if _, err := tx.Exec(ctx, `UPDATE outbox SET sent_at = now() WHERE id = ANY($1)`, ids); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	r.log.Debug("outbox relayed", slog.Int("count", len(records)))
	return len(records), nil
}
