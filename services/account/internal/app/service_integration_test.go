//go:build integration

package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/aidostt/bank-core/pkg/apperr"
	"github.com/aidostt/bank-core/pkg/grpcx"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/aidostt/bank-core/pkg/pgtx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/aidostt/bank-core/services/account/internal/adapters/postgres"
	"github.com/aidostt/bank-core/services/account/internal/domain"
	"github.com/aidostt/bank-core/services/account/migrations"
)

// fakeLedger implements the app.LedgerClient port in memory.
type fakeLedger struct {
	mu       sync.Mutex
	accounts map[string]string // external id → currency
}

func newFakeLedger() *fakeLedger { return &fakeLedger{accounts: map[string]string{}} }

func (f *fakeLedger) CreateAccount(_ context.Context, id, currency string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accounts[id] = currency
	return nil
}

func (f *fakeLedger) GetBalances(_ context.Context, ids []string) (map[string]BalanceView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]BalanceView{}
	for _, id := range ids {
		if _, ok := f.accounts[id]; ok {
			out[id] = BalanceView{AccountID: id, AsOf: time.Now()}
		}
	}
	return out, nil
}

func (f *fakeLedger) ListPostings(context.Context, string, time.Time, time.Time, int32, string) ([]TransactionView, string, error) {
	return nil, "", nil
}

func startPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("account_db"),
		tcpostgres.WithUsername("account_user"),
		tcpostgres.WithPassword("account_pass"),
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
	return pool
}

func TestOpenResolveFreezeFlow(t *testing.T) {
	pool := startPostgres(t)
	ctx := context.Background()
	ledger := newFakeLedger()
	svc := NewService(postgres.NewStore(pool), ledger, logging.New("test"))

	alice := grpcx.Claims{CustomerID: uuid.NewString(), Roles: []string{"customer"}}
	bob := grpcx.Claims{CustomerID: uuid.NewString(), Roles: []string{"customer"}}
	admin := grpcx.Claims{CustomerID: uuid.NewString(), Roles: []string{"admin"}}

	// open: creates customer lazily, ledger mirror synchronously
	acc, err := svc.OpenAccount(ctx, alice, "KZT")
	if err != nil {
		t.Fatal(err)
	}
	if !domain.ValidNumber(acc.Number) {
		t.Fatalf("invalid number %s", acc.Number)
	}
	if ledger.accounts[acc.ID] != "KZT" {
		t.Fatal("ledger mirror not created before return")
	}

	// currency validation
	if _, err := svc.OpenAccount(ctx, alice, "EUR"); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("EUR accepted: %v", err)
	}

	// resolve by number — visible to any caller (P2P destination)
	got, err := svc.ResolveByNumber(ctx, acc.Number)
	if err != nil || got.ID != acc.ID {
		t.Fatalf("resolve: %v", err)
	}

	// ownership: bob cannot read alice's account
	if _, err := svc.GetAccount(ctx, bob, acc.ID); apperr.CodeOf(err) != apperr.CodeForbidden {
		t.Fatalf("ownership not enforced: %v", err)
	}
	// staff can
	if _, err := svc.GetAccount(ctx, admin, acc.ID); err != nil {
		t.Fatalf("staff read: %v", err)
	}

	// freeze: staff only
	if _, err := svc.Freeze(ctx, alice, acc.ID, "susp"); apperr.CodeOf(err) != apperr.CodeForbidden {
		t.Fatalf("customer froze account: %v", err)
	}
	frozen, err := svc.Freeze(ctx, admin, acc.ID, "fraud alert")
	if err != nil || frozen.Status != domain.StatusFrozen {
		t.Fatalf("freeze: %v %+v", err, frozen)
	}
	// idempotent freeze
	if _, err := svc.Freeze(ctx, admin, acc.ID, "again"); err != nil {
		t.Fatalf("second freeze: %v", err)
	}
	unfrozen, err := svc.Unfreeze(ctx, admin, acc.ID)
	if err != nil || unfrozen.Status != domain.StatusActive {
		t.Fatalf("unfreeze: %v", err)
	}

	// list with balances (M1: proxied to ledger)
	items, err := svc.ListAccounts(ctx, alice, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Balance == nil {
		t.Fatalf("list: %+v", items)
	}

	// second open for same user reuses the customer row
	acc2, err := svc.OpenAccount(ctx, alice, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if acc2.CustomerID != acc.CustomerID {
		t.Fatal("customer row duplicated")
	}
}
