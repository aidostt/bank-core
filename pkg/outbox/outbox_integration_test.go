//go:build integration

package outbox_test

import (
	"context"
	"testing"
	"time"

	commonv1 "github.com/aidostt/bank-core/gen/go/bank/common/v1"
	eventsv1 "github.com/aidostt/bank-core/gen/go/bank/events/v1"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/aidostt/bank-core/pkg/outbox"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/modules/redpanda"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
)

const topic = "ledger.transactions"

func startPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("ob_db"), tcpostgres.WithUsername("ob"), tcpostgres.WithPassword("ob"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("5432/tcp")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	var pool *pgxpool.Pool
	deadline := time.Now().Add(30 * time.Second)
	for {
		pool, err = pgxpool.New(ctx, dsn)
		if err == nil && pool.Ping(ctx) == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("postgres: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `CREATE TABLE outbox (
		id uuid PRIMARY KEY, topic text, key text, payload bytea,
		created_at timestamptz NOT NULL DEFAULT now(), sent_at timestamptz)`)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func startRedpanda(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	rp, err := redpanda.Run(ctx, "redpandadata/redpanda:v24.2.18")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rp.Terminate(context.Background()) })
	broker, err := rp.KafkaSeedBroker(ctx)
	if err != nil {
		t.Fatal(err)
	}
	admc, err := kgo.NewClient(kgo.SeedBrokers(broker))
	if err != nil {
		t.Fatal(err)
	}
	defer admc.Close()
	if _, err := kadm.NewClient(admc).CreateTopics(ctx, 1, 1, nil, topic); err != nil {
		t.Fatal(err)
	}
	return broker
}

// Insert writes inside the caller's tx; the relay publishes pending rows and
// marks them sent (ADR-0009).
func TestInsertAndRelay(t *testing.T) {
	pool := startPostgres(t)
	broker := startRedpanda(t)
	ctx := context.Background()

	err := pgxTx(ctx, pool, func(tx pgx.Tx) error {
		for i := 0; i < 3; i++ {
			m, err := outbox.NewProtoMessage(ctx, topic, "acct", "req", &commonv1.Money{MinorUnits: int64(i), Currency: "KZT"})
			if err != nil {
				return err
			}
			if err := outbox.Insert(ctx, tx, m); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	relay, err := outbox.NewRelay(pool, []string{broker}, logging.New("relay-test"))
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	relayCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go relay.Run(relayCtx)

	consumer, err := kgo.NewClient(kgo.SeedBrokers(broker), kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()

	got := 0
	deadline := time.Now().Add(30 * time.Second)
	for got < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("consumed %d/3", got)
		}
		fctx, fcancel := context.WithTimeout(ctx, 5*time.Second)
		fetches := consumer.PollFetches(fctx)
		fcancel()
		fetches.EachRecord(func(r *kgo.Record) {
			var env eventsv1.EventEnvelope
			if proto.Unmarshal(r.Value, &env) == nil && env.GetEventId() != "" {
				got++
			}
		})
	}

	// All rows eventually marked sent.
	deadline = time.Now().Add(15 * time.Second)
	for {
		var unsent int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE sent_at IS NULL`).Scan(&unsent); err != nil {
			t.Fatal(err)
		}
		if unsent == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d rows never marked sent", unsent)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func pgxTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
