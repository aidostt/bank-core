//go:build integration

package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/aidostt/bank-core/pkg/pgtx"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/aidostt/bank-core/services/ledger/internal/adapters/postgres"
	"github.com/aidostt/bank-core/services/ledger/internal/app"
	"github.com/aidostt/bank-core/services/ledger/migrations"
)

func startLedgerDB(t *testing.T) (*pgxpool.Pool, *postgres.Store, *app.Service) {
	t.Helper()
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("ledger_db"),
		tcpostgres.WithUsername("ledger_user"),
		tcpostgres.WithPassword("ledger_pass"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("5432/tcp")),
	)
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

	if err := postgres.EnsurePartitions(ctx, pool, time.Now().UTC()); err != nil {
		t.Fatalf("partitions: %v", err)
	}
	store := postgres.NewStore(pool)
	svc := app.NewService(store, 10*time.Minute, logging.New("test"))
	return pool, store, svc
}

// seedCustomer creates a ledger account and tops it up from cash_in_kzt.
func seedCustomer(t *testing.T, svc *app.Service, externalID string, amount int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := svc.CreateAccount(ctx, externalID, "KZT"); err != nil {
		t.Fatal(err)
	}
	if amount == 0 {
		return
	}
	_, err := svc.PostTransaction(ctx, "topup", "seed-"+externalID, "", []app.PostingSpec{
		{Ref: app.AccountRef{InternalCode: "cash_in_kzt"}, Amount: -amount, Currency: "KZT"},
		{Ref: app.AccountRef{ExternalID: externalID}, Amount: amount, Currency: "KZT"},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func balanceOf(t *testing.T, svc *app.Service, ref app.AccountRef) app.BalanceRow {
	t.Helper()
	rows, err := svc.GetBalances(context.Background(), []app.AccountRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 balance row, got %d", len(rows))
	}
	return rows[0]
}
