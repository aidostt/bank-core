//go:build integration

package kafka

import (
	"context"
	"testing"
	"time"

	eventsv1 "github.com/aidostt/bank-core/gen/go/bank/events/v1"
	ledgerv1 "github.com/aidostt/bank-core/gen/go/bank/ledger/v1"
	kafkart "github.com/aidostt/bank-core/pkg/kafka"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/aidostt/bank-core/pkg/outbox"
	"github.com/aidostt/bank-core/pkg/pgtx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aidostt/bank-core/services/account/migrations"
)

func startDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("account_db"),
		tcpostgres.WithUsername("account_user"),
		tcpostgres.WithPassword("account_pass"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("5432/tcp")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err = pgtx.Migrate(dsn, migrations.FS, "."); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("migrate: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	pool, err := pgtx.Connect(ctx, dsn, logging.New("test"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// deliver runs one event through the handler inside a real transaction,
// exactly the way the consumer runtime does.
func deliver(t *testing.T, pool *pgxpool.Pool, h *Handlers, payload proto.Message) {
	t.Helper()
	ctx := context.Background()
	out, err := outbox.NewProtoMessage(ctx, "test.topic", "k", "req", payload)
	if err != nil {
		t.Fatal(err)
	}
	env := &eventsv1.EventEnvelope{}
	if err := proto.Unmarshal(out.Payload, env); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := h.Handle(ctx, tx, kafkart.Message{
		EventID: env.GetEventId(), Envelope: env, Topic: "test.topic",
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func seedAccount(t *testing.T, pool *pgxpool.Pool) (accountID string) {
	t.Helper()
	ctx := context.Background()
	accountID = uuid.NewString()
	userID := uuid.NewString()
	var customerID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO customers (user_id) VALUES ($1) RETURNING id`, userID).Scan(&customerID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO accounts (id, customer_id, number, currency) VALUES ($1, $2, $3, 'KZT')`,
		accountID, customerID, "KZ00"+uuid.NewString()[:16]); err != nil {
		t.Fatal(err)
	}
	return accountID
}

func posted(accountID string, balance, version int64, at time.Time) *eventsv1.TransactionPosted {
	return &eventsv1.TransactionPosted{
		Entry:             &ledgerv1.JournalEntry{OccurredAt: timestamppb.New(at)},
		AccountId:         uuid.NewString(),
		ExternalAccountId: accountID,
		Currency:          "KZT",
		BalanceAfter:      balance,
		Version:           version,
	}
}

// M2 DoD: out-of-order events (v3 before v2) — the projection keeps v3.
func TestProjectionOutOfOrder(t *testing.T) {
	pool := startDB(t)
	h := NewHandlers(logging.New("test"))
	accountID := seedAccount(t, pool)
	now := time.Now().UTC()

	deliver(t, pool, h, posted(accountID, 100, 1, now))
	deliver(t, pool, h, posted(accountID, 300, 3, now.Add(2*time.Second))) // v3 arrives first
	deliver(t, pool, h, posted(accountID, 200, 2, now.Add(time.Second)))   // stale v2 must be ignored

	var balance, version int64
	if err := pool.QueryRow(context.Background(),
		`SELECT balance, version FROM balances WHERE account_id = $1`, accountID).Scan(&balance, &version); err != nil {
		t.Fatal(err)
	}
	if balance != 300 || version != 3 {
		t.Fatalf("projection = (%d, v%d), want (300, v3)", balance, version)
	}
}

// HIGH fraud alert freezes the account and queues AccountFrozen; MEDIUM
// does not; a repeat freeze is idempotent.
func TestFreezeOnHighAlert(t *testing.T) {
	pool := startDB(t)
	h := NewHandlers(logging.New("test"))
	accountID := seedAccount(t, pool)
	ctx := context.Background()

	deliver(t, pool, h, &eventsv1.FraudAlertRaised{
		CustomerId: uuid.NewString(), TransferId: uuid.NewString(),
		Rule: "R1", Severity: "MEDIUM", AccountId: accountID,
	})
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM accounts WHERE id=$1`, accountID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "ACTIVE" {
		t.Fatalf("MEDIUM alert froze the account: %s", status)
	}

	deliver(t, pool, h, &eventsv1.FraudAlertRaised{
		CustomerId: uuid.NewString(), TransferId: uuid.NewString(),
		Rule: "R2", Severity: "HIGH", AccountId: accountID,
	})
	if err := pool.QueryRow(ctx, `SELECT status FROM accounts WHERE id=$1`, accountID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "FROZEN" {
		t.Fatalf("HIGH alert did not freeze: %s", status)
	}
	var outboxRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE topic='accounts.events'`).Scan(&outboxRows); err != nil {
		t.Fatal(err)
	}
	if outboxRows != 1 {
		t.Fatalf("AccountFrozen outbox rows = %d, want 1", outboxRows)
	}

	// idempotent repeat
	deliver(t, pool, h, &eventsv1.FraudAlertRaised{
		CustomerId: uuid.NewString(), TransferId: uuid.NewString(),
		Rule: "R2", Severity: "HIGH", AccountId: accountID,
	})
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE topic='accounts.events'`).Scan(&outboxRows); err != nil {
		t.Fatal(err)
	}
	if outboxRows != 1 {
		t.Fatalf("repeat freeze produced another event: %d rows", outboxRows)
	}
}

// CustomerRegistered bootstraps the customer row idempotently.
func TestCustomerBootstrap(t *testing.T) {
	pool := startDB(t)
	h := NewHandlers(logging.New("test"))
	userID := uuid.NewString()

	deliver(t, pool, h, &eventsv1.CustomerRegistered{UserId: userID, Email: "a@b.kz"})
	deliver(t, pool, h, &eventsv1.CustomerRegistered{UserId: userID, Email: "a@b.kz"})

	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM customers WHERE user_id=$1`, userID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("customer rows = %d, want 1", n)
	}
}
