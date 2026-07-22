//go:build integration

package app_test

import (
	"context"
	"testing"
	"time"

	commonv1 "github.com/aidostt/bank-core/gen/go/bank/common/v1"
	eventsv1 "github.com/aidostt/bank-core/gen/go/bank/events/v1"
	transferv1 "github.com/aidostt/bank-core/gen/go/bank/transfer/v1"
	kafkart "github.com/aidostt/bank-core/pkg/kafka"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/aidostt/bank-core/pkg/pgtx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/aidostt/bank-core/services/antifraud/internal/app"
	"github.com/aidostt/bank-core/services/antifraud/migrations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// outboxMessage wraps a proto payload in the standard envelope, as the
// producer's outbox relay would deliver it to the consumer runtime.
func outboxMessage(payload proto.Message) (kafkart.Message, error) {
	any, err := anypb.New(payload)
	if err != nil {
		return kafkart.Message{}, err
	}
	now := time.Now().UTC()
	return kafkart.Message{
		EventID:    uuid.NewString(),
		OccurredAt: now,
		Envelope: &eventsv1.EventEnvelope{
			EventId:    uuid.NewString(),
			OccurredAt: timestamppb.New(now),
			Payload:    any,
		},
	}, nil
}

func newDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("antifraud_db"), tcpostgres.WithUsername("af"), tcpostgres.WithPassword("af"),
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

func completed(customer, from, to, currency string, amount int64) kafkart.Message {
	view := &transferv1.TransferView{
		Id:            uuid.NewString(),
		Type:          transferv1.TransferType_TRANSFER_TYPE_P2P,
		CustomerId:    customer,
		FromAccountId: from,
		ToAccountId:   to,
		Amount:        &commonv1.Money{MinorUnits: amount, Currency: currency},
	}
	msg, _ := outboxMessage(&eventsv1.TransferCompleted{Transfer: view})
	return msg
}

func score(t *testing.T, pool *pgxpool.Pool, scorer *app.Scorer, msg kafkart.Message) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := scorer.Handle(ctx, tx, msg); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func alertsFor(t *testing.T, pool *pgxpool.Pool, customer string) map[string]string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT rule_id, severity FROM alerts WHERE customer_id=$1`, customer)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var rule, sev string
		if err := rows.Scan(&rule, &sev); err != nil {
			t.Fatal(err)
		}
		out[rule] = sev
	}
	return out
}

func TestScorerAmountAndNewBeneficiary(t *testing.T) {
	pool := newDB(t)
	scorer := app.NewScorer(logging.New("test"))
	cust := uuid.NewString()

	// 20M KZT to a brand-new beneficiary: R1 (>10M, MEDIUM) and R4
	// (new beneficiary + >5M, MEDIUM) both fire.
	score(t, pool, scorer, completed(cust, uuid.NewString(), uuid.NewString(), "KZT", 20_000_000))

	got := alertsFor(t, pool, cust)
	if got["R1"] != "MEDIUM" {
		t.Errorf("R1 amount_over not raised: %v", got)
	}
	if got["R4"] != "MEDIUM" {
		t.Errorf("R4 new_beneficiary not raised: %v", got)
	}
	// Outbox event emitted per alert.
	var outboxN int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM outbox WHERE key=$1`, cust).Scan(&outboxN); err != nil {
		t.Fatal(err)
	}
	if outboxN < 2 {
		t.Fatalf("expected >=2 outbox alerts, got %d", outboxN)
	}
}

func TestScorerVelocityHigh(t *testing.T) {
	pool := newDB(t)
	scorer := app.NewScorer(logging.New("test"))
	cust := uuid.NewString()
	to := uuid.NewString()

	// 11 small transfers within the 5m window; R2 fires when count > 10.
	for i := 0; i < 11; i++ {
		score(t, pool, scorer, completed(cust, uuid.NewString(), to, "KZT", 100))
	}
	if alertsFor(t, pool, cust)["R2"] != "HIGH" {
		t.Fatalf("R2 velocity HIGH not raised after 11 transfers: %v", alertsFor(t, pool, cust))
	}
}

func TestScorerIgnoresTopupAndFailures(t *testing.T) {
	pool := newDB(t)
	scorer := app.NewScorer(logging.New("test"))
	cust := uuid.NewString()

	// A huge TOPUP must not be scored (incoming funding, not spend).
	topup := &transferv1.TransferView{
		Id: uuid.NewString(), Type: transferv1.TransferType_TRANSFER_TYPE_TOPUP,
		CustomerId: cust, ToAccountId: uuid.NewString(),
		Amount: &commonv1.Money{MinorUnits: 999_000_000, Currency: "KZT"},
	}
	topMsg, _ := outboxMessage(&eventsv1.TransferCompleted{Transfer: topup})
	score(t, pool, scorer, topMsg)

	// A TransferFailed envelope is a foreign type — skipped.
	failMsg, _ := outboxMessage(&eventsv1.TransferFailed{
		Transfer: &transferv1.TransferView{Id: uuid.NewString(), CustomerId: cust},
		Reason:   "x",
	})
	if err := func() error {
		ctx := context.Background()
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := scorer.Handle(ctx, tx, failMsg); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}(); err != nil {
		t.Fatal(err)
	}

	if len(alertsFor(t, pool, cust)) != 0 {
		t.Fatalf("topup/failed must raise no alerts: %v", alertsFor(t, pool, cust))
	}
	_ = pgx.ErrNoRows
}
