//go:build integration

package app_test

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

	"github.com/aidostt/bank-core/services/transfer/internal/adapters/accountclient"
	"github.com/aidostt/bank-core/services/transfer/internal/adapters/ledgerclient"
	"github.com/aidostt/bank-core/services/transfer/internal/adapters/postgres"
	"github.com/aidostt/bank-core/services/transfer/internal/app"
	"github.com/aidostt/bank-core/services/transfer/internal/domain"
	"github.com/aidostt/bank-core/services/transfer/migrations"
)

type fixture struct {
	pool    *pgxpool.Pool
	svc     *app.Service
	ledger  *fakeLedger
	account *fakeAccount
}

func setup(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("transfer_db"),
		tcpostgres.WithUsername("transfer_user"),
		tcpostgres.WithPassword("transfer_pass"),
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

	ledger := newFakeLedger()
	account := newFakeAccount()
	lAddr, aAddr := serveFakes(t, ledger, account)
	lc, err := ledgerclient.New(lAddr, logging.New("test"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lc.Close() })
	ac, err := accountclient.New(aAddr, logging.New("test"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ac.Close() })

	svc := app.NewService(postgres.NewStore(pool), lc, ac, logging.New("test"))
	return &fixture{pool: pool, svc: svc, ledger: ledger, account: account}
}

func (f *fixture) eventChain(t *testing.T, transferID string) []string {
	t.Helper()
	rows, err := f.pool.Query(context.Background(),
		`SELECT to_state FROM transfer_events WHERE transfer_id = $1 ORDER BY id`, transferID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var chain []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		chain = append(chain, s)
	}
	return chain
}

func TestHappyPathP2P(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice, bob := uuid.NewString(), uuid.NewString()
	src := f.account.add(alice, "KZT", "ACTIVE")
	dst := f.account.add(bob, "KZT", "ACTIVE")
	f.ledger.balances[src.Id] = 100_000

	tr, err := f.svc.CreateTransfer(ctx, app.CreateCmd{
		CustomerID: alice, IdempotencyKey: "k1", Type: domain.TypeP2P,
		FromAccountID: src.Id, ToAccountNumber: dst.Number,
		Amount: 25_000, Currency: "KZT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tr.State != domain.StateCompleted {
		t.Fatalf("state = %s, reason %v", tr.State, tr.Reason)
	}
	if tr.ToAccountID != dst.Id {
		t.Fatalf("destination not resolved: %s", tr.ToAccountID)
	}
	// Audit chain (ADR-0010): every state change appended.
	want := []string{"CREATED", "VALIDATING", "HELD", "POSTING", "COMPLETED"}
	got := f.eventChain(t, tr.ID)
	if len(got) != len(want) {
		t.Fatalf("event chain %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event chain %v, want %v", got, want)
		}
	}
	// Terminal outbox event written in the same tx.
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE key = $1`, tr.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("outbox rows: %d", n)
	}
}

func TestInsufficientFundsFailsCleanly(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice, bob := uuid.NewString(), uuid.NewString()
	src := f.account.add(alice, "KZT", "ACTIVE")
	dst := f.account.add(bob, "KZT", "ACTIVE")
	f.ledger.balances[src.Id] = 100 // way too little

	tr, err := f.svc.CreateTransfer(ctx, app.CreateCmd{
		CustomerID: alice, IdempotencyKey: "k1", Type: domain.TypeP2P,
		FromAccountID: src.Id, ToAccountNumber: dst.Number,
		Amount: 25_000, Currency: "KZT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tr.State != domain.StateFailed || tr.Reason == nil || *tr.Reason != "INSUFFICIENT_FUNDS" {
		t.Fatalf("state=%s reason=%v", tr.State, tr.Reason)
	}
}

func TestFrozenDestinationFailsValidation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice, bob := uuid.NewString(), uuid.NewString()
	src := f.account.add(alice, "KZT", "ACTIVE")
	dst := f.account.add(bob, "KZT", "FROZEN")
	f.ledger.balances[src.Id] = 100_000

	tr, err := f.svc.CreateTransfer(ctx, app.CreateCmd{
		CustomerID: alice, IdempotencyKey: "k1", Type: domain.TypeP2P,
		FromAccountID: src.Id, ToAccountNumber: dst.Number,
		Amount: 1_000, Currency: "KZT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tr.State != domain.StateFailed || *tr.Reason != "ACCOUNT_FROZEN" {
		t.Fatalf("state=%s reason=%v", tr.State, tr.Reason)
	}
	// No money side effects: hold never placed.
	if len(f.ledger.holds) != 0 {
		t.Fatal("hold placed for failed validation")
	}
}

// FX conversion uses the seeded USDKZT rate with banker's rounding
// (pkg/money): 10 cents × 478.25 = 4782.5 tiyn → 4782 (ties to even).
func TestFXConversionAndRounding(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := uuid.NewString()
	usd := f.account.add(alice, "USD", "ACTIVE")
	kzt := f.account.add(alice, "KZT", "ACTIVE")
	f.ledger.balances[usd.Id] = 1_000_000

	tr, err := f.svc.CreateTransfer(ctx, app.CreateCmd{
		CustomerID: alice, IdempotencyKey: "fx1", Type: domain.TypeInternal,
		FromAccountID: usd.Id, ToAccountID: kzt.Id,
		Amount: 10, Currency: "USD",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tr.State != domain.StateCompleted {
		t.Fatalf("state=%s reason=%v", tr.State, tr.Reason)
	}
	if tr.CounterAmount == nil || *tr.CounterAmount != 4_782 {
		t.Fatalf("counter amount = %v, want 4782 (banker's rounding)", tr.CounterAmount)
	}
	if tr.RateMicros == nil || *tr.RateMicros != 478_250_000 {
		t.Fatalf("applied rate = %v", tr.RateMicros)
	}
	// KZT side landed on the destination in the fake ledger.
	if got := f.ledger.balances[kzt.Id]; got != 4_782 {
		t.Fatalf("destination credited %d", got)
	}
}

func TestLimitExceeded(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice, bob := uuid.NewString(), uuid.NewString()
	src := f.account.add(alice, "KZT", "ACTIVE")
	dst := f.account.add(bob, "KZT", "ACTIVE")
	f.ledger.balances[src.Id] = 1 << 40

	// per_tx for KZT is 100_000_000 tiyn (migration seed)
	tr, err := f.svc.CreateTransfer(ctx, app.CreateCmd{
		CustomerID: alice, IdempotencyKey: "big", Type: domain.TypeP2P,
		FromAccountID: src.Id, ToAccountNumber: dst.Number,
		Amount: 100_000_001, Currency: "KZT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tr.State != domain.StateFailed || *tr.Reason != "LIMIT_EXCEEDED" {
		t.Fatalf("state=%s reason=%v", tr.State, tr.Reason)
	}
}

// ADR-0012 DoD: same key, 20 parallel requests → exactly one transfer.
func TestIdempotencyUnderConcurrency(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice, bob := uuid.NewString(), uuid.NewString()
	src := f.account.add(alice, "KZT", "ACTIVE")
	dst := f.account.add(bob, "KZT", "ACTIVE")
	f.ledger.balances[src.Id] = 1_000_000

	cmd := app.CreateCmd{
		CustomerID: alice, IdempotencyKey: "parallel-key", Type: domain.TypeP2P,
		FromAccountID: src.Id, ToAccountNumber: dst.Number,
		Amount: 5_000, Currency: "KZT",
	}
	const n = 20
	var wg sync.WaitGroup
	results := make([]*app.Transfer, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = f.svc.CreateTransfer(ctx, cmd)
		}(i)
	}
	wg.Wait()

	firstID := ""
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("request %d: %v", i, errs[i])
		}
		if firstID == "" {
			firstID = results[i].ID
		} else if results[i].ID != firstID {
			t.Fatalf("two transfers created: %s vs %s", firstID, results[i].ID)
		}
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM transfers`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("transfer rows: %d, want 1", count)
	}
	// The fake ledger must have exactly one entry (posted once).
	if len(f.ledger.entries) != 1 {
		t.Fatalf("ledger entries: %d", len(f.ledger.entries))
	}
	// Money moved exactly once.
	if got := f.ledger.balances[dst.Id]; got != 5_000 {
		t.Fatalf("destination credited %d, want 5000", got)
	}
}

func TestIdempotencyConflictOnDifferentPayload(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice, bob := uuid.NewString(), uuid.NewString()
	src := f.account.add(alice, "KZT", "ACTIVE")
	dst := f.account.add(bob, "KZT", "ACTIVE")
	f.ledger.balances[src.Id] = 1_000_000

	cmd := app.CreateCmd{
		CustomerID: alice, IdempotencyKey: "the-key", Type: domain.TypeP2P,
		FromAccountID: src.Id, ToAccountNumber: dst.Number,
		Amount: 5_000, Currency: "KZT",
	}
	if _, err := f.svc.CreateTransfer(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	cmd.Amount = 6_000
	_, err := f.svc.CreateTransfer(ctx, cmd)
	if apperr.CodeOf(err) != apperr.CodeIdempotencyConflict {
		t.Fatalf("want IDEMPOTENCY_CONFLICT, got %v", err)
	}
}

func TestOwnershipEnforced(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice, bob := uuid.NewString(), uuid.NewString()
	bobsAccount := f.account.add(bob, "KZT", "ACTIVE")
	dst := f.account.add(alice, "KZT", "ACTIVE")
	f.ledger.balances[bobsAccount.Id] = 1_000_000

	// alice tries to move money out of bob's account
	tr, err := f.svc.CreateTransfer(ctx, app.CreateCmd{
		CustomerID: alice, IdempotencyKey: "steal", Type: domain.TypeP2P,
		FromAccountID: bobsAccount.Id, ToAccountNumber: dst.Number,
		Amount: 5_000, Currency: "KZT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tr.State != domain.StateFailed || *tr.Reason != "NOT_ACCOUNT_OWNER" {
		t.Fatalf("state=%s reason=%v", tr.State, tr.Reason)
	}
	// GetTransfer ownership
	if _, err := f.svc.GetTransfer(ctx, grpcx.Claims{CustomerID: bob, Roles: []string{"customer"}}, tr.ID); apperr.CodeOf(err) != apperr.CodeForbidden {
		t.Fatalf("foreign GetTransfer: %v", err)
	}
}
