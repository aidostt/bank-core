//go:build integration

package grpc_test

import (
	"context"
	"testing"
	"time"

	commonv1 "github.com/aidostt/bank-core/gen/go/bank/common/v1"
	ledgerv1 "github.com/aidostt/bank-core/gen/go/bank/ledger/v1"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/aidostt/bank-core/pkg/pgtx"
	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	grpcadapter "github.com/aidostt/bank-core/services/ledger/internal/adapters/grpc"
	"github.com/aidostt/bank-core/services/ledger/internal/adapters/postgres"
	"github.com/aidostt/bank-core/services/ledger/internal/app"
	"github.com/aidostt/bank-core/services/ledger/migrations"
)

func newServer(t *testing.T) *grpcadapter.Server {
	t.Helper()
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("ledger_db"), tcpostgres.WithUsername("lg"), tcpostgres.WithPassword("lg"),
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
	if err := postgres.EnsurePartitions(ctx, pool, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	svc := app.NewService(postgres.NewStore(pool), 10*time.Minute, logging.New("test"))
	return grpcadapter.NewServer(svc)
}

func ext(id string) *ledgerv1.AccountRef {
	return &ledgerv1.AccountRef{Ref: &ledgerv1.AccountRef_ExternalAccountId{ExternalAccountId: id}}
}
func internal(code string) *ledgerv1.AccountRef {
	return &ledgerv1.AccountRef{Ref: &ledgerv1.AccountRef_InternalCode{InternalCode: code}}
}
func kzt(v int64) *commonv1.Money { return &commonv1.Money{MinorUnits: v, Currency: "KZT"} }

func TestLedgerGRPCSurface(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	acct := uuid.NewString()

	if _, err := srv.CreateAccount(ctx, &ledgerv1.CreateAccountRequest{ExternalAccountId: acct, Currency: "KZT"}); err != nil {
		t.Fatal(err)
	}
	// unsupported currency → FailedPrecondition (CURRENCY_MISMATCH)
	if _, err := srv.CreateAccount(ctx, &ledgerv1.CreateAccountRequest{ExternalAccountId: uuid.NewString(), Currency: "EUR"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("bad currency: %v", err)
	}

	// top-up posts a balanced entry from cash_in.
	post, err := srv.PostTransaction(ctx, &ledgerv1.PostTransactionRequest{
		ReferenceType: "topup", ReferenceId: "tp-1",
		Postings: []*ledgerv1.PostingSpec{
			{Account: internal("cash_in_kzt"), Amount: kzt(-50_000)},
			{Account: ext(acct), Amount: kzt(50_000)},
		},
	})
	if err != nil || len(post.GetEntry().GetPostings()) != 2 {
		t.Fatalf("post: %v", err)
	}
	// idempotent replay returns the same entry
	replay, err := srv.PostTransaction(ctx, &ledgerv1.PostTransactionRequest{
		ReferenceType: "topup", ReferenceId: "tp-1",
		Postings: []*ledgerv1.PostingSpec{
			{Account: internal("cash_in_kzt"), Amount: kzt(-50_000)},
			{Account: ext(acct), Amount: kzt(50_000)},
		},
	})
	if err != nil || replay.GetEntry().GetId() != post.GetEntry().GetId() {
		t.Fatalf("replay: %v", err)
	}
	// unbalanced entry → FailedPrecondition
	if _, err := srv.PostTransaction(ctx, &ledgerv1.PostTransactionRequest{
		ReferenceType: "bad", ReferenceId: "b-1",
		Postings: []*ledgerv1.PostingSpec{
			{Account: internal("cash_in_kzt"), Amount: kzt(-1)},
			{Account: ext(acct), Amount: kzt(2)},
		},
	}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unbalanced: %v", err)
	}

	// balances reflect the posting
	bal, err := srv.GetBalances(ctx, &ledgerv1.GetBalancesRequest{Accounts: []*ledgerv1.AccountRef{ext(acct)}})
	if err != nil || bal.GetBalances()[0].GetBalance() != 50_000 {
		t.Fatalf("balances: %v", err)
	}

	// hold lifecycle
	hold, err := srv.PlaceHold(ctx, &ledgerv1.PlaceHoldRequest{
		Account: ext(acct), Amount: kzt(10_000), ReferenceType: "transfer", ReferenceId: "h-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.ReleaseHold(ctx, &ledgerv1.ReleaseHoldRequest{HoldId: hold.GetHold().GetId()}); err != nil {
		t.Fatal(err)
	}

	// GetTransactionByReference: found + not found
	if _, err := srv.GetTransactionByReference(ctx, &ledgerv1.GetTransactionByReferenceRequest{ReferenceType: "topup", ReferenceId: "tp-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.GetTransactionByReference(ctx, &ledgerv1.GetTransactionByReferenceRequest{ReferenceType: "topup", ReferenceId: "nope"}); status.Code(err) != codes.NotFound {
		t.Fatalf("missing ref: %v", err)
	}

	// ListPostings with a time range
	now := time.Now().UTC()
	lp, err := srv.ListPostings(ctx, &ledgerv1.ListPostingsRequest{
		Account: ext(acct), From: timestamppb.New(now.Add(-time.Hour)), To: timestamppb.New(now.Add(time.Hour)), PageSize: 10,
	})
	if err != nil || len(lp.GetPostings()) == 0 {
		t.Fatalf("listpostings: %v n=%d", err, len(lp.GetPostings()))
	}
}
