//go:build integration

package app_test

import (
	"context"
	"testing"

	"github.com/aidostt/bank-core/pkg/grpcx"
	"github.com/google/uuid"

	"github.com/aidostt/bank-core/services/transfer/internal/app"
	"github.com/aidostt/bank-core/services/transfer/internal/domain"
)

// ListTransfers paginates newest-first by (created_at, id) cursor, owner-scoped.
func TestListTransfersPagination(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice, bob := uuid.NewString(), uuid.NewString()
	src := f.account.add(alice, "KZT", "ACTIVE")
	dst := f.account.add(bob, "KZT", "ACTIVE")
	f.ledger.balances[src.Id] = 1_000_000

	for i := 0; i < 4; i++ {
		if _, err := f.svc.CreateTransfer(ctx, app.CreateCmd{
			CustomerID: alice, IdempotencyKey: uuid.NewString(), Type: domain.TypeP2P,
			FromAccountID: src.Id, ToAccountNumber: dst.Number, Amount: 100, Currency: "KZT",
		}); err != nil {
			t.Fatal(err)
		}
	}

	owner := grpcx.Claims{CustomerID: alice, Roles: []string{"customer"}}
	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		page, next, err := f.svc.ListTransfers(ctx, owner, 2, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, tr := range page {
			seen[tr.ID] = true
		}
		pages++
		if next == "" {
			break
		}
		cursor = next
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
	}
	if len(seen) != 4 || pages < 2 {
		t.Fatalf("paginated %d transfers over %d pages, want 4 over >=2", len(seen), pages)
	}

	// A stranger sees none of alice's transfers.
	strangerPage, _, err := f.svc.ListTransfers(ctx, grpcx.Claims{CustomerID: uuid.NewString(), Roles: []string{"customer"}}, 10, "")
	if err != nil || len(strangerPage) != 0 {
		t.Fatalf("owner scoping: %v n=%d", err, len(strangerPage))
	}
	// Malformed cursor rejected.
	if _, _, err := f.svc.ListTransfers(ctx, owner, 2, "!!bad!!"); err == nil {
		t.Fatal("want error for malformed cursor")
	}
}

func TestGetRatesAndCleanup(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	rates, err := f.svc.GetRates(ctx)
	if err != nil || len(rates) == 0 {
		t.Fatalf("rates: %v", err)
	}
	if rates[0].Pair != "USDKZT" || rates[0].BuyMicros == 0 {
		t.Fatalf("seeded rate: %+v", rates[0])
	}
	// Cleanup runs without error (nothing is 24h old yet, so it removes none).
	f.svc.CleanupIdempotencyKeys(ctx)
}
