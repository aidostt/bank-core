//go:build integration

package kafka_test

import (
	"context"
	"errors"
	"testing"
	"time"

	eventsv1 "github.com/aidostt/bank-core/gen/go/bank/events/v1"
	kafkart "github.com/aidostt/bank-core/pkg/kafka"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/aidostt/bank-core/pkg/outbox"
	"github.com/aidostt/bank-core/pkg/pgtx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/modules/redpanda"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/twmb/franz-go/pkg/kgo"
)

func setupInfra(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	ctx := context.Background()

	pg, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("consumer_db"),
		tcpostgres.WithUsername("u"), tcpostgres.WithPassword("p"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("5432/tcp")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgtx.Connect(ctx, dsn, logging.New("test"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, err = pool.Exec(ctx, `
			CREATE TABLE IF NOT EXISTS processed_messages (
				consumer_group text NOT NULL, event_id uuid NOT NULL,
				processed_at timestamptz NOT NULL DEFAULT now(),
				PRIMARY KEY (consumer_group, event_id));
			CREATE TABLE IF NOT EXISTS side_effects (
				event_id uuid PRIMARY KEY, at timestamptz NOT NULL DEFAULT now());`)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Auto-create mirrors the compose broker config (DLQ topics are created
	// on first produce — ADR-0008 creates the business topics explicitly).
	rp, err := redpanda.Run(ctx, "redpandadata/redpanda:v24.2.18",
		redpanda.WithAutoCreateTopics())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rp.Terminate(context.Background()) })
	broker, err := rp.KafkaSeedBroker(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return pool, broker
}

func produceEnvelope(t *testing.T, broker, topic string, msg outbox.Message) {
	t.Helper()
	client, err := kgo.NewClient(kgo.SeedBrokers(broker), kgo.AllowAutoTopicCreation())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.ProduceSync(context.Background(),
		&kgo.Record{Topic: topic, Key: []byte(msg.Key), Value: msg.Payload}).FirstErr(); err != nil {
		t.Fatal(err)
	}
}

// M2 DoD: the same event delivered twice produces exactly one side effect.
func TestDuplicateDeliveryOneSideEffect(t *testing.T) {
	pool, broker := setupInfra(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handler := func(ctx context.Context, tx pgx.Tx, msg kafkart.Message) error {
		_, err := tx.Exec(ctx, `INSERT INTO side_effects (event_id) VALUES ($1)`, msg.EventID)
		return err
	}
	consumer, err := kafkart.NewConsumer(kafkart.Config{
		Brokers: []string{broker}, Group: "dup-test", Topics: []string{"dup.topic"},
		BackoffBase: 10 * time.Millisecond,
	}, pool, handler, nil, logging.New("test"))
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	go consumer.Run(ctx)

	msg, err := outbox.NewProtoMessage(ctx, "dup.topic", "k", "req-1",
		&eventsv1.CustomerRegistered{UserId: "u-1"})
	if err != nil {
		t.Fatal(err)
	}
	// Exactly the same bytes twice — an outbox redelivery.
	produceEnvelope(t, broker, "dup.topic", msg)
	produceEnvelope(t, broker, "dup.topic", msg)

	deadline := time.Now().Add(30 * time.Second)
	for {
		var processed int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM processed_messages`).Scan(&processed); err != nil {
			t.Fatal(err)
		}
		if processed >= 1 {
			// give the duplicate a moment to be (not) applied
			time.Sleep(2 * time.Second)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("event never processed")
		}
		time.Sleep(200 * time.Millisecond)
	}
	var effects int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM side_effects`).Scan(&effects); err != nil {
		t.Fatal(err)
	}
	if effects != 1 {
		t.Fatalf("side effects = %d, want exactly 1", effects)
	}
}

// M2 DoD: a poison message reaches <group>.<topic>.dlq with attempts/error
// headers after the retry budget.
func TestPoisonMessageReachesDLQ(t *testing.T) {
	pool, broker := setupInfra(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	poison := errors.New("handler exploded")
	handler := func(ctx context.Context, tx pgx.Tx, msg kafkart.Message) error {
		return poison
	}
	consumer, err := kafkart.NewConsumer(kafkart.Config{
		Brokers: []string{broker}, Group: "poison-test", Topics: []string{"poison.topic"},
		BackoffBase: 10 * time.Millisecond, // 10,20,40,80,160ms — fast test
	}, pool, handler, nil, logging.New("test"))
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	go consumer.Run(ctx)

	msg, err := outbox.NewProtoMessage(ctx, "poison.topic", "k", "req-1",
		&eventsv1.CustomerRegistered{UserId: "u-poison"})
	if err != nil {
		t.Fatal(err)
	}
	produceEnvelope(t, broker, "poison.topic", msg)

	dlqClient, err := kgo.NewClient(
		kgo.SeedBrokers(broker),
		kgo.ConsumeTopics("poison-test.poison.topic.dlq"),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer dlqClient.Close()

	deadline := time.Now().Add(60 * time.Second)
	var record *kgo.Record
	for record == nil {
		if time.Now().After(deadline) {
			t.Fatal("poison message never reached the DLQ")
		}
		fctx, fcancel := context.WithTimeout(ctx, 5*time.Second)
		fetches := dlqClient.PollFetches(fctx)
		fcancel()
		fetches.EachRecord(func(r *kgo.Record) { record = r })
	}

	headers := map[string]string{}
	for _, h := range record.Headers {
		headers[h.Key] = string(h.Value)
	}
	if headers["attempts"] != "5" {
		t.Fatalf("attempts header = %q, want 5", headers["attempts"])
	}
	if headers["error"] == "" || headers["first_seen"] == "" || headers["source_topic"] != "poison.topic" {
		t.Fatalf("headers incomplete: %v", headers)
	}
	if _, err := time.Parse(time.RFC3339, headers["first_seen"]); err != nil {
		t.Fatalf("first_seen not RFC3339: %v", err)
	}
	// The original payload is preserved for replay.
	if string(record.Value) != string(msg.Payload) {
		t.Fatal("DLQ payload differs from the original")
	}
	// No side effect row: the dedup marker rolled back with each failure.
	var processed int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM processed_messages`).Scan(&processed); err != nil {
		t.Fatal(err)
	}
	if processed != 0 {
		t.Fatalf("processed_messages = %d, want 0 for a poisoned event", processed)
	}
}
