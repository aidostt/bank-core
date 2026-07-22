//go:build integration

package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	commonv1 "github.com/aidostt/bank-core/gen/go/bank/common/v1"
	eventsv1 "github.com/aidostt/bank-core/gen/go/bank/events/v1"
	transferv1 "github.com/aidostt/bank-core/gen/go/bank/transfer/v1"
	kafkart "github.com/aidostt/bank-core/pkg/kafka"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/aidostt/bank-core/pkg/pgtx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aidostt/bank-core/services/notification/internal/app"
	"github.com/aidostt/bank-core/services/notification/migrations"
)

type captureSender struct {
	fail bool
	sent int
}

func (c *captureSender) Channel() string { return "capture" }
func (c *captureSender) Send(context.Context, string, string) error {
	if c.fail {
		return errors.New("delivery down")
	}
	c.sent++
	return nil
}

func newDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("notification_db"), tcpostgres.WithUsername("nt"), tcpostgres.WithPassword("nt"),
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

func wrap(t *testing.T, payload proto.Message) kafkart.Message {
	t.Helper()
	any, err := anypb.New(payload)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	return kafkart.Message{
		OccurredAt: now,
		Envelope:   &eventsv1.EventEnvelope{EventId: uuid.NewString(), OccurredAt: timestamppb.New(now), Payload: any},
	}
}

func handle(t *testing.T, pool *pgxpool.Pool, n *app.Notifier, msg kafkart.Message) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := n.Handle(ctx, tx, msg); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func feed(t *testing.T, pool *pgxpool.Pool, user string) (template, status string, n int) {
	t.Helper()
	err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM notifications WHERE user_id=$1`, user).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n > 0 {
		_ = pool.QueryRow(context.Background(),
			`SELECT template, status FROM notifications WHERE user_id=$1 ORDER BY created_at DESC LIMIT 1`, user).
			Scan(&template, &status)
	}
	return
}

func TestNotifierRendersEachEventType(t *testing.T) {
	pool := newDB(t)
	n := app.NewNotifier(&captureSender{}, logging.New("test"))

	u1 := uuid.NewString()
	handle(t, pool, n, wrap(t, &eventsv1.TransferCompleted{Transfer: &transferv1.TransferView{
		Id: uuid.NewString(), CustomerId: u1, Type: transferv1.TransferType_TRANSFER_TYPE_P2P,
		Amount: &commonv1.Money{MinorUnits: 150000, Currency: "KZT"},
	}}))
	if tpl, st, cnt := feed(t, pool, u1); cnt != 1 || tpl != "transfer_completed" || st != "sent" {
		t.Fatalf("completed: cnt=%d tpl=%s st=%s", cnt, tpl, st)
	}

	u2 := uuid.NewString()
	handle(t, pool, n, wrap(t, &eventsv1.TransferFailed{
		Transfer: &transferv1.TransferView{Id: uuid.NewString(), CustomerId: u2,
			Amount: &commonv1.Money{MinorUnits: 1, Currency: "KZT"}},
		Reason: "INSUFFICIENT_FUNDS",
	}))
	if tpl, _, cnt := feed(t, pool, u2); cnt != 1 || tpl != "transfer_failed" {
		t.Fatalf("failed: cnt=%d tpl=%s", cnt, tpl)
	}

	u3 := uuid.NewString()
	handle(t, pool, n, wrap(t, &eventsv1.FraudAlertRaised{
		CustomerId: u3, Rule: "R2", Severity: "HIGH", TransferId: uuid.NewString(),
	}))
	if tpl, _, cnt := feed(t, pool, u3); cnt != 1 || tpl != "fraud_alert" {
		t.Fatalf("fraud: cnt=%d tpl=%s", cnt, tpl)
	}
}

func TestNotifierIgnoresForeignEvent(t *testing.T) {
	pool := newDB(t)
	n := app.NewNotifier(&captureSender{}, logging.New("test"))
	// AccountFrozen is not a notification trigger.
	handle(t, pool, n, wrap(t, &eventsv1.AccountFrozen{AccountId: uuid.NewString(), Reason: "x"}))
	var total int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Fatalf("foreign event created %d rows", total)
	}
}

func TestNotifierRecordsFailedDelivery(t *testing.T) {
	pool := newDB(t)
	n := app.NewNotifier(&captureSender{fail: true}, logging.New("test"))
	u := uuid.NewString()
	handle(t, pool, n, wrap(t, &eventsv1.TransferCompleted{Transfer: &transferv1.TransferView{
		Id: uuid.NewString(), CustomerId: u, Type: transferv1.TransferType_TRANSFER_TYPE_P2P,
		Amount: &commonv1.Money{MinorUnits: 1, Currency: "KZT"},
	}}))
	// The row is still persisted, marked failed (feed doubles as audit).
	if _, st, cnt := feed(t, pool, u); cnt != 1 || st != "failed" {
		t.Fatalf("failed delivery: cnt=%d st=%s", cnt, st)
	}
}
