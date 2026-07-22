package ledgerclient_test

import (
	"context"
	"net"
	"testing"
	"time"

	commonv1 "github.com/aidostt/bank-core/gen/go/bank/common/v1"
	ledgerv1 "github.com/aidostt/bank-core/gen/go/bank/ledger/v1"
	"github.com/aidostt/bank-core/pkg/apperr"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aidostt/bank-core/services/account/internal/adapters/ledgerclient"
)

// fakeLedger implements just the RPCs the account ledger client calls.
type fakeLedger struct {
	ledgerv1.UnimplementedLedgerServiceServer
	failCreate bool
}

func (f *fakeLedger) CreateAccount(_ context.Context, req *ledgerv1.CreateAccountRequest) (*ledgerv1.CreateAccountResponse, error) {
	if f.failCreate {
		return nil, apperr.ToGRPC(apperr.New(apperr.CodeCurrencyMismatch, "bad currency"))
	}
	return &ledgerv1.CreateAccountResponse{Account: &ledgerv1.LedgerAccount{Id: req.GetExternalAccountId()}}, nil
}

func (f *fakeLedger) GetBalances(_ context.Context, req *ledgerv1.GetBalancesRequest) (*ledgerv1.GetBalancesResponse, error) {
	out := &ledgerv1.GetBalancesResponse{}
	for _, r := range req.GetAccounts() {
		out.Balances = append(out.Balances, &ledgerv1.AccountBalance{
			ExternalAccountId: r.GetExternalAccountId(), Currency: "KZT",
			Balance: 1000, Held: 100, Available: 900, AsOf: timestamppb.Now(),
		})
	}
	return out, nil
}

func (f *fakeLedger) ListPostings(_ context.Context, _ *ledgerv1.ListPostingsRequest) (*ledgerv1.ListPostingsResponse, error) {
	return &ledgerv1.ListPostingsResponse{
		Postings: []*ledgerv1.Posting{{
			Id: uuid.NewString(), ExternalAccountId: "a", Amount: &commonv1.Money{MinorUnits: 5, Currency: "KZT"},
			OccurredAt: timestamppb.Now(),
		}},
		NextCursor: "next",
	}, nil
}

func serve(t *testing.T, f *fakeLedger) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := grpc.NewServer()
	ledgerv1.RegisterLedgerServiceServer(s, f)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	return lis.Addr().String()
}

func TestLedgerClient(t *testing.T) {
	client, err := ledgerclient.New(serve(t, &fakeLedger{}), logging.New("test"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	ctx := context.Background()

	if err := client.CreateAccount(ctx, uuid.NewString(), "KZT"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	bals, err := client.GetBalances(ctx, []string{"a1", "a2"})
	if err != nil || len(bals) != 2 || bals["a1"].Available != 900 {
		t.Fatalf("GetBalances: %v %+v", err, bals)
	}
	txs, next, err := client.ListPostings(ctx, "a1", time.Time{}, time.Now(), 10, "")
	if err != nil || len(txs) != 1 || next != "next" {
		t.Fatalf("ListPostings: %v n=%d next=%q", err, len(txs), next)
	}
}

func TestLedgerClientMapsErrors(t *testing.T) {
	client, err := ledgerclient.New(serve(t, &fakeLedger{failCreate: true}), logging.New("test"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	// The typed error survives the round trip through apperr.
	if err := client.CreateAccount(context.Background(), uuid.NewString(), "KZT"); apperr.CodeOf(err) != apperr.CodeCurrencyMismatch {
		t.Fatalf("error mapping: %v", err)
	}
}
