//go:build integration

package grpc_test

import (
	"context"
	"net"
	"testing"
	"time"

	accountv1 "github.com/aidostt/bank-core/gen/go/bank/account/v1"
	commonv1 "github.com/aidostt/bank-core/gen/go/bank/common/v1"
	ledgerv1 "github.com/aidostt/bank-core/gen/go/bank/ledger/v1"
	transferv1 "github.com/aidostt/bank-core/gen/go/bank/transfer/v1"
	"github.com/aidostt/bank-core/pkg/grpcx"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/aidostt/bank-core/pkg/pgtx"
	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aidostt/bank-core/services/transfer/internal/adapters/accountclient"
	grpcadapter "github.com/aidostt/bank-core/services/transfer/internal/adapters/grpc"
	"github.com/aidostt/bank-core/services/transfer/internal/adapters/ledgerclient"
	"github.com/aidostt/bank-core/services/transfer/internal/adapters/postgres"
	"github.com/aidostt/bank-core/services/transfer/internal/app"
	"github.com/aidostt/bank-core/services/transfer/migrations"
)

// --- fake downstreams (happy path) ---

type fakeLedger struct {
	ledgerv1.UnimplementedLedgerServiceServer
}

func (fakeLedger) PlaceHold(_ context.Context, r *ledgerv1.PlaceHoldRequest) (*ledgerv1.PlaceHoldResponse, error) {
	return &ledgerv1.PlaceHoldResponse{Hold: &ledgerv1.Hold{Id: uuid.NewString(), ReferenceId: r.GetReferenceId()}}, nil
}
func (fakeLedger) PostTransaction(_ context.Context, r *ledgerv1.PostTransactionRequest) (*ledgerv1.PostTransactionResponse, error) {
	return &ledgerv1.PostTransactionResponse{Entry: &ledgerv1.JournalEntry{ReferenceId: r.GetReferenceId()}}, nil
}

type fakeAccount struct {
	accountv1.UnimplementedAccountServiceServer
	from, to *accountv1.Account
}

func (f fakeAccount) GetAccount(_ context.Context, r *accountv1.GetAccountRequest) (*accountv1.GetAccountResponse, error) {
	if r.GetAccountId() == f.from.GetId() {
		return &accountv1.GetAccountResponse{Account: f.from}, nil
	}
	return nil, status.Error(codes.NotFound, "no account")
}
func (f fakeAccount) ResolveByNumber(_ context.Context, r *accountv1.ResolveByNumberRequest) (*accountv1.ResolveByNumberResponse, error) {
	if r.GetNumber() == f.to.GetNumber() {
		return &accountv1.ResolveByNumberResponse{Account: f.to}, nil
	}
	return nil, status.Error(codes.NotFound, "no account")
}

func serveGRPC(t *testing.T, reg func(*grpc.Server)) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := grpc.NewServer()
	reg(s)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	return lis.Addr().String()
}

func newServer(t *testing.T, customer string) (*grpcadapter.Server, string) {
	t.Helper()
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("transfer_db"), tcpostgres.WithUsername("tr"), tcpostgres.WithPassword("tr"),
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

	from := &accountv1.Account{Id: uuid.NewString(), UserId: customer, Number: "KZ-FROM", Currency: "KZT", Status: "ACTIVE"}
	to := &accountv1.Account{Id: uuid.NewString(), UserId: uuid.NewString(), Number: "KZ-TO", Currency: "KZT", Status: "ACTIVE"}
	lAddr := serveGRPC(t, func(s *grpc.Server) { ledgerv1.RegisterLedgerServiceServer(s, fakeLedger{}) })
	aAddr := serveGRPC(t, func(s *grpc.Server) { accountv1.RegisterAccountServiceServer(s, fakeAccount{from: from, to: to}) })
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
	return grpcadapter.NewServer(svc), from.GetId()
}

func customerCtx(customer, idemKey string) context.Context {
	ctx := grpcx.ContextWithClaims(context.Background(), grpcx.Claims{CustomerID: customer, Roles: []string{"customer"}})
	return grpcx.ContextWithIdempotencyKey(ctx, idemKey)
}

func TestTransferGRPCSurface(t *testing.T) {
	customer := uuid.NewString()
	srv, fromID := newServer(t, customer)

	// GetRates works and returns the seeded USDKZT pair.
	rates, err := srv.GetRates(context.Background(), &transferv1.GetRatesRequest{})
	if err != nil || len(rates.GetRates()) == 0 {
		t.Fatalf("rates: %v", err)
	}

	// Missing idempotency key → InvalidArgument (IDEMPOTENCY_KEY_REQUIRED).
	noKey := grpcx.ContextWithClaims(context.Background(), grpcx.Claims{CustomerID: customer, Roles: []string{"customer"}})
	if _, err := srv.CreateTransfer(noKey, &transferv1.CreateTransferRequest{
		Type: transferv1.TransferType_TRANSFER_TYPE_P2P, FromAccountId: fromID,
		ToAccountNumber: "KZ-TO", Amount: &commonv1.Money{MinorUnits: 100, Currency: "KZT"},
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing idem key: %v", err)
	}

	// Happy P2P completes synchronously.
	resp, err := srv.CreateTransfer(customerCtx(customer, "idem-1"), &transferv1.CreateTransferRequest{
		Type: transferv1.TransferType_TRANSFER_TYPE_P2P, FromAccountId: fromID,
		ToAccountNumber: "KZ-TO", Amount: &commonv1.Money{MinorUnits: 100, Currency: "KZT"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tr := resp.GetTransfer()
	if tr.GetState() != transferv1.TransferState_TRANSFER_STATE_COMPLETED {
		t.Fatalf("state = %s", tr.GetState())
	}

	// GetTransfer (owner) + ListTransfers.
	got, err := srv.GetTransfer(customerCtx(customer, ""), &transferv1.GetTransferRequest{TransferId: tr.GetId()})
	if err != nil || got.GetTransfer().GetId() != tr.GetId() {
		t.Fatalf("get: %v", err)
	}
	list, err := srv.ListTransfers(customerCtx(customer, ""), &transferv1.ListTransfersRequest{PageSize: 10})
	if err != nil || len(list.GetTransfers()) != 1 {
		t.Fatalf("list: %v n=%d", err, len(list.GetTransfers()))
	}
	// Foreign customer cannot read it.
	if _, err := srv.GetTransfer(customerCtx(uuid.NewString(), ""), &transferv1.GetTransferRequest{TransferId: tr.GetId()}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ownership: %v", err)
	}
}
