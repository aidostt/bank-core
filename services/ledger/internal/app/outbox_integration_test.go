//go:build integration

package app_test

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	eventsv1 "github.com/aidostt/bank-core/gen/go/bank/events/v1"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/aidostt/bank-core/pkg/outbox"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	"github.com/aidostt/bank-core/services/ledger/internal/app"
)

// startRedpandaFixedPort runs Redpanda with an explicit host-port binding
// and a matching advertised address. The stock testcontainers module wires
// the advertised listener to the randomly mapped port at Run() time, which
// a docker stop/start silently remaps — breaking any kill/restart scenario.
// A fixed binding survives restarts, like the stable DNS name in compose.
func startRedpandaFixedPort(t *testing.T) (testcontainers.Container, string) {
	t.Helper()
	ctx := context.Background()
	port := freePort(t)
	portSpec := fmt.Sprintf("%d/tcp", port)
	req := testcontainers.ContainerRequest{
		Image:        "redpandadata/redpanda:v24.2.18",
		ExposedPorts: []string{portSpec},
		HostConfigModifier: func(hc *container.HostConfig) {
			hc.PortBindings = network.PortMap{
				network.MustParsePort(portSpec): []network.PortBinding{{HostPort: fmt.Sprintf("%d", port)}},
			}
		},
		Cmd: []string{
			"redpanda", "start", "--mode=dev-container", "--smp=1", "--memory=512M",
			fmt.Sprintf("--kafka-addr=external://0.0.0.0:%d", port),
			fmt.Sprintf("--advertise-kafka-addr=external://localhost:%d", port),
		},
		WaitingFor: wait.ForLog("Successfully started Redpanda").WithStartupTimeout(90 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	return container, fmt.Sprintf("localhost:%d", port)
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// Outbox relay test (ledger doc, Testing focus): events reach the broker
// with the envelope intact; a broker outage delays delivery but never
// blocks writes (architecture §7); delivery resumes after restart.
func TestOutboxRelayDeliversAndSurvivesBrokerRestart(t *testing.T) {
	pool, _, svc := startLedgerDB(t)
	ctx := context.Background()

	rp, broker := startRedpandaFixedPort(t)

	// Topics are created explicitly (ADR-0008) — compose does it via the
	// redpanda-init job, tests do it here.
	createTopic(t, broker)

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

	// Broker-side proof of delivery before consuming.
	waitForEndOffsets(t, broker, 2, 30*time.Second)

	records := consumeN(t, broker, 2, 30*time.Second)
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

	// Broker down: posting still succeeds (outbox accumulates) — the key
	// outbox property: the system stays available for writes.
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
	// The outage-time rows sit unsent while the broker is down.
	var unsent int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE sent_at IS NULL`).Scan(&unsent); err != nil {
		t.Fatal(err)
	}
	if unsent == 0 {
		t.Fatal("outage rows marked sent while broker was down")
	}

	if err := rp.Start(ctx); err != nil {
		t.Fatal(err)
	}
	// The fixed host-port binding survives the restart, so the original
	// relay client reconnects on its own — exactly the compose behaviour
	// where the broker address is a stable DNS name.

	// Delivery resumes: 2 more events for the outage-time entry.
	waitForEndOffsets(t, broker, 4, 90*time.Second)
	records = consumeN(t, broker, 4, 60*time.Second)
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

func createTopic(t *testing.T, broker string) {
	t.Helper()
	client, err := kgo.NewClient(kgo.SeedBrokers(broker))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	resp, err := kadm.NewClient(client).CreateTopics(context.Background(), 6, 1, nil, app.TopicTransactions)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range resp.Sorted() {
		if r.Err != nil {
			t.Fatalf("create topic %s: %v", r.Topic, r.Err)
		}
	}
}

// waitForEndOffsets polls the broker until the topic holds ≥ n records.
func waitForEndOffsets(t *testing.T, broker string, n int64, timeout time.Duration) {
	t.Helper()
	client, err := kgo.NewClient(kgo.SeedBrokers(broker))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	adm := kadm.NewClient(client)
	deadline := time.Now().Add(timeout)
	for {
		var total int64
		offsets, err := adm.ListEndOffsets(context.Background(), app.TopicTransactions)
		if err == nil {
			offsets.Each(func(o kadm.ListedOffset) {
				if o.Err == nil {
					total += o.Offset
				}
			})
		}
		if total >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("broker holds %d records, want ≥%d (list err: %v)", total, n, err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// consumeN reads with a fresh client from the start of the topic.
func consumeN(t *testing.T, broker string, n int, timeout time.Duration) []*kgo.Record {
	t.Helper()
	client, err := kgo.NewClient(
		kgo.SeedBrokers(broker),
		kgo.ConsumeTopics(app.TopicTransactions),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
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
