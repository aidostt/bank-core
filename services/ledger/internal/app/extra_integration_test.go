//go:build integration

package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/google/uuid"

	"github.com/aidostt/bank-core/services/ledger/internal/app"
	"github.com/aidostt/bank-core/services/ledger/internal/domain"
)

// The sweeper releases holds past their expiry so a dead orchestrator cannot
// strand reserved funds (ledger doc, Failure & ops).
func TestSweeperReleasesExpiredHolds(t *testing.T) {
	pool, _, svc := startLedgerDB(t)
	ctx := context.Background()
	alice := uuid.NewString()
	seedCustomer(t, svc, alice, 10_000)
	ref := app.AccountRef{ExternalID: alice}

	hold, err := svc.PlaceHold(ctx, ref, 4_000, "KZT", "transfer", "sweep-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if b := balanceOf(t, svc, ref); b.Held != 4_000 {
		t.Fatalf("held before sweep: %d", b.Held)
	}
	// Force the hold into the past.
	if _, err := pool.Exec(ctx, `UPDATE holds SET expires_at = now() - interval '1 minute' WHERE id=$1`, hold.ID); err != nil {
		t.Fatal(err)
	}

	sweeper := app.NewSweeper(svc, 100*time.Millisecond, logging.New("test"))
	sweepCtx, cancel := context.WithCancel(ctx)
	go sweeper.Run(sweepCtx)
	defer cancel()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if b := balanceOf(t, svc, ref); b.Held == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sweeper never released the expired hold")
		}
		time.Sleep(150 * time.Millisecond)
	}
	// Releasing an already-released hold is a no-op.
	if _, err := svc.ReleaseHold(ctx, hold.ID); err != nil {
		t.Fatalf("idempotent release: %v", err)
	}
	_ = domain.HoldReleased
}

// ListPostings paginates by (occurred_at, id) cursor.
func TestListPostingsPagination(t *testing.T) {
	_, _, svc := startLedgerDB(t)
	ctx := context.Background()
	alice := uuid.NewString()
	seedCustomer(t, svc, alice, 0)

	// Five separate topups → five postings on alice's account.
	for i := 0; i < 5; i++ {
		if _, err := svc.PostTransaction(ctx, "topup", uuid.NewString(), "", []app.PostingSpec{
			{Ref: app.AccountRef{InternalCode: "cash_in_kzt"}, Amount: -100, Currency: "KZT"},
			{Ref: app.AccountRef{ExternalID: alice}, Amount: 100, Currency: "KZT"},
		}); err != nil {
			t.Fatal(err)
		}
	}

	from := time.Now().UTC().Add(-time.Hour)
	to := time.Now().UTC().Add(time.Hour)
	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		page, next, err := svc.ListPostings(ctx, app.AccountRef{ExternalID: alice}, from, to, 2, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range page {
			if seen[p.ID] {
				t.Fatalf("duplicate posting across pages: %s", p.ID)
			}
			seen[p.ID] = true
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
	if len(seen) != 5 || pages < 3 {
		t.Fatalf("paginated %d postings over %d pages, want 5 over >=3", len(seen), pages)
	}

	// A malformed cursor is rejected.
	if _, _, err := svc.ListPostings(ctx, app.AccountRef{ExternalID: alice}, from, to, 2, "!!not-base64!!"); err == nil {
		t.Fatal("want error for malformed cursor")
	}
	// A missing time range is rejected (partition pruning, ADR-0017).
	if _, _, err := svc.ListPostings(ctx, app.AccountRef{ExternalID: alice}, time.Time{}, time.Time{}, 2, ""); err == nil {
		t.Fatal("want error for missing time range")
	}
}
