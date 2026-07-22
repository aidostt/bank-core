//go:build integration

package grpc_test

import (
	"context"
	"testing"
	"time"

	accountv1 "github.com/aidostt/bank-core/gen/go/bank/account/v1"
	"github.com/aidostt/bank-core/pkg/grpcx"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/aidostt/bank-core/pkg/pgtx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	grpcadapter "github.com/aidostt/bank-core/services/account/internal/adapters/grpc"
	"github.com/aidostt/bank-core/services/account/internal/adapters/postgres"
	"github.com/aidostt/bank-core/services/account/internal/app"
	"github.com/aidostt/bank-core/services/account/migrations"
)

type fakeLedger struct{ bal map[string]int64 }

func (f *fakeLedger) CreateAccount(_ context.Context, id, _ string) error {
	if f.bal == nil {
		f.bal = map[string]int64{}
	}
	f.bal[id] = 0
	return nil
}
func (f *fakeLedger) GetBalances(_ context.Context, ids []string) (map[string]app.BalanceView, error) {
	out := map[string]app.BalanceView{}
	for _, id := range ids {
		out[id] = app.BalanceView{AccountID: id, AsOf: time.Now()}
	}
	return out, nil
}
func (f *fakeLedger) ListPostings(context.Context, string, time.Time, time.Time, int32, string) ([]app.TransactionView, string, error) {
	return nil, "", nil
}

func newServer(t *testing.T) *grpcadapter.Server {
	t.Helper()
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("account_db"), tcpostgres.WithUsername("ac"), tcpostgres.WithPassword("ac"),
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
	var pool *pgxpool.Pool
	pool, err = pgtx.Connect(ctx, dsn, logging.New("test"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	svc := app.NewService(postgres.NewStore(pool), &fakeLedger{}, logging.New("test"))
	return grpcadapter.NewServer(svc)
}

func ctxAs(customer string, roles ...string) context.Context {
	return grpcx.ContextWithClaims(context.Background(), grpcx.Claims{CustomerID: customer, Roles: roles})
}

// Drives the whole gRPC surface through the real store to cover the adapter
// translation + error mapping (ADR-0018).
func TestAccountGRPCSurface(t *testing.T) {
	srv := newServer(t)
	alice := uuid.NewString()
	admin := uuid.NewString()

	// unauthenticated open → Unauthenticated
	if _, err := srv.OpenAccount(context.Background(), &accountv1.OpenAccountRequest{Currency: "KZT"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("anon open: %v", err)
	}
	// bad currency → InvalidArgument
	if _, err := srv.OpenAccount(ctxAs(alice, "customer"), &accountv1.OpenAccountRequest{Currency: "EUR"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad currency: %v", err)
	}

	open, err := srv.OpenAccount(ctxAs(alice, "customer"), &accountv1.OpenAccountRequest{Currency: "KZT"})
	if err != nil {
		t.Fatal(err)
	}
	acc := open.GetAccount()
	if acc.GetNumber() == "" || acc.GetStatus() != "ACTIVE" {
		t.Fatalf("opened account: %+v", acc)
	}

	// GetAccount by owner, and ResolveByNumber
	if _, err := srv.GetAccount(ctxAs(alice, "customer"), &accountv1.GetAccountRequest{AccountId: acc.GetId()}); err != nil {
		t.Fatal(err)
	}
	if r, err := srv.ResolveByNumber(context.Background(), &accountv1.ResolveByNumberRequest{Number: acc.GetNumber()}); err != nil || r.GetAccount().GetId() != acc.GetId() {
		t.Fatalf("resolve: %v", err)
	}
	// foreign customer read → PermissionDenied
	if _, err := srv.GetAccount(ctxAs(uuid.NewString(), "customer"), &accountv1.GetAccountRequest{AccountId: acc.GetId()}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ownership: %v", err)
	}
	// not found → NotFound
	if _, err := srv.GetAccount(ctxAs(alice, "customer"), &accountv1.GetAccountRequest{AccountId: uuid.NewString()}); status.Code(err) != codes.NotFound {
		t.Fatalf("missing: %v", err)
	}

	// list + balances
	list, err := srv.ListAccountsByCustomer(ctxAs(alice, "customer"), &accountv1.ListAccountsByCustomerRequest{})
	if err != nil || len(list.GetAccounts()) != 1 {
		t.Fatalf("list: %v n=%d", err, len(list.GetAccounts()))
	}
	if _, err := srv.GetBalances(ctxAs(alice, "customer"), &accountv1.GetBalancesRequest{AccountIds: []string{acc.GetId()}}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.ListTransactions(ctxAs(alice, "customer"), &accountv1.ListTransactionsRequest{AccountId: acc.GetId()}); err != nil {
		t.Fatal(err)
	}

	// freeze requires staff
	if _, err := srv.Freeze(ctxAs(alice, "customer"), &accountv1.FreezeRequest{AccountId: acc.GetId()}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("customer freeze: %v", err)
	}
	frozen, err := srv.Freeze(ctxAs(admin, "admin"), &accountv1.FreezeRequest{AccountId: acc.GetId(), Reason: "fraud"})
	if err != nil || frozen.GetAccount().GetStatus() != "FROZEN" {
		t.Fatalf("freeze: %v", err)
	}
	back, err := srv.Unfreeze(ctxAs(admin, "admin"), &accountv1.UnfreezeRequest{AccountId: acc.GetId()})
	if err != nil || back.GetAccount().GetStatus() != "ACTIVE" {
		t.Fatalf("unfreeze: %v", err)
	}
}
