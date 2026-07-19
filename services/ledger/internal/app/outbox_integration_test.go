//go:build integration

package app_test

import (
	"context"
	"testing"
	"time"

	eventsv1 "github.com/aidostt/bank-core/gen/go/bank/events/v1"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/aidostt/bank-core/pkg/outbox"
	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go/modules/redpanda"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	"github.com/aidostt/bank-core/services/ledger/internal/app"
)

// Outbox relay test (ledger doc, Testing focus): events reach the broker
// with the envelope intact; a broker outage delays delivery but never
// blocks writes (architecture §7); delivery resumes after restart.
func TestOutboxRelayDeliversAndSurvivesBrokerRestart(t *testing.T) {
	pool, _, svc := startLedgerDB(t)
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

	// Topics are created explicitly (ADR-0008) — compose does it via the
	// redpanda-init job, tests do it here.
	admClient, err := kgo.NewClient(kgo.SeedBrokers(broker))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kadm.NewClient(admClient).CreateTopics(ctx, 6, 1, nil, app.TopicTransactions); err != nil {
		admClient.Close()
		t.Fatal(err)
	}
	admClient.Close()

	relayCtx, cancelRelay := context.WithCancel(ctx)
	defer cancelRelay()
	relay, err := outbox.NewRelay(pool, []string{broker}, logging.New("relay-test"))
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	go relay.Run(relayCtx)

	alice := uuid.NewString()
	seedCustomer(t, svc, alice, 50_000) // 2 affected accounts → 2 events

	consumer, err := kgo.NewClient(
		kgo.SeedBrokers(broker),
		kgo.ConsumeTopics(app.TopicTransactions),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()

	records := consumeN(t, consumer, 2, 30*time.Second)
	for _, rec := range records {
		var env eventsv1.EventEnvelope
		if err := proto.Unmarshal(rec.Value, &env); err != nil {
			t.Fatalf("envelope unmarshal: %v", err)
		}
		if env.GetEventId() == "" || env.GetOccurredAt() == nil {
			t.Fatalf("envelope incomplete: %+v", &env)
		}
		var posted eventsv1.TransactionPosted
		if err := env.GetPayload().UnmarshalTo(&posted); err != nil {
			t.Fatalf("payload: %v", err)
		}
		if posted.GetEntry().GetReferenceId() != "seed-"+alice {
			t.Fatalf("unexpected entry reference: %s", posted.GetEntry().GetReferenceId())
		}
	}

	// Broker down: posting still succeeds (outbox accumulates).
	stopTimeout := 10 * time.Second
	if err := rp.Stop(ctx, &stopTimeout); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PostTransaction(ctx, "transfer", "during-outage", "", []app.PostingSpec{
		{Ref: app.AccountRef{ExternalID: alice}, Amount: -1_000, Currency: "KZT"},
		{Ref: app.AccountRef{InternalCode: "cash_in_kzt"}, Amount: 1_000, Currency: "KZT"},
	}); err != nil {
		t.Fatalf("write blocked by broker outage: %v", err)
	}

	if err := rp.Start(ctx); err != nil {
		t.Fatal(err)
	}
	// Delivery resumes: 2 more events for the outage-time entry.
	records = consumeN(t, consumer, 2, 60*time.Second)
	found := false
	for _, rec := range records {
		var env eventsv1.EventEnvelope
		if err := proto.Unmarshal(rec.Value, &env); err != nil {
			continue
		}
		var posted eventsv1.TransactionPosted
		if err := env.GetPayload().UnmarshalTo(&posted); err != nil {
			continue
		}
		if posted.GetEntry().GetReferenceId() == "during-outage" {
			found = true
		}
	}
	if !found {
		t.Fatal("outage-time entry never delivered after broker restart")
	}

	// Every outbox row is eventually marked sent.
	deadline := time.Now().Add(30 * time.Second)
	for {
		var unsent int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE sent_at IS NULL`).Scan(&unsent); err != nil {
			t.Fatal(err)
		}
		if unsent == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d outbox rows never marked sent", unsent)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func consumeN(t *testing.T, client *kgo.Client, n int, timeout time.Duration) []*kgo.Record {
	t.Helper()
	var out []*kgo.Record
	deadline := time.Now().Add(timeout)
	for len(out) < n {
		if time.Now().After(deadline) {
			t.Fatalf("consumed %d/%d records before timeout", len(out), n)
		}
		fetchCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		fetches := client.PollFetches(fetchCtx)
		cancel()
		fetches.EachError(func(topic string, partition int32, err error) {
			t.Logf("fetch error on %s/%d: %v", topic, partition, err)
		})
		fetches.EachRecord(func(r *kgo.Record) { out = append(out, r) })
	}
	return out
}
