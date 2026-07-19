//go:build integration

package app_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aidostt/bank-core/pkg/apperr"
	"github.com/google/uuid"

	"github.com/aidostt/bank-core/services/ledger/internal/app"
)

// Invariant 6: PostTransaction is idempotent by reference — replay returns
// the same entry, balances move once.
func TestPostTransactionIdempotentReplay(t *testing.T) {
	_, _, svc := startLedgerDB(t)
	ctx := context.Background()
	alice := uuid.NewString()
	seedCustomer(t, svc, alice, 10_000)

	specs := []app.PostingSpec{
		{Ref: app.AccountRef{ExternalID: alice}, Amount: -1_000, Currency: "KZT"},
		{Ref: app.AccountRef{InternalCode: "cash_in_kzt"}, Amount: 1_000, Currency: "KZT"},
	}
	first, err := svc.PostTransaction(ctx, "transfer", "tx-1", "", specs)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := svc.PostTransaction(ctx, "transfer", "tx-1", "", specs)
	if err != nil {
		t.Fatal(err)
	}
	if replay.ID != first.ID {
		t.Fatalf("replay produced a different entry: %s vs %s", replay.ID, first.ID)
	}
	if b := balanceOf(t, svc, app.AccountRef{ExternalID: alice}); b.Balance != 9_000 {
		t.Fatalf("balance moved twice: %d", b.Balance)
	}

	// Same reference, different payload → ALREADY_EXISTS.
	_, err = svc.PostTransaction(ctx, "transfer", "tx-1", "", []app.PostingSpec{
		{Ref: app.AccountRef{ExternalID: alice}, Amount: -2_000, Currency: "KZT"},
		{Ref: app.AccountRef{InternalCode: "cash_in_kzt"}, Amount: 2_000, Currency: "KZT"},
	})
	if apperr.CodeOf(err) != apperr.CodeAlreadyExists {
		t.Fatalf("want ALREADY_EXISTS, got %v", err)
	}
}

// Concurrent duplicates: same reference from many goroutines → exactly one
// entry, every caller gets the same result.
func TestPostTransactionConcurrentSameReference(t *testing.T) {
	pool, _, svc := startLedgerDB(t)
	ctx := context.Background()
	alice := uuid.NewString()
	seedCustomer(t, svc, alice, 100_000)

	specs := []app.PostingSpec{
		{Ref: app.AccountRef{ExternalID: alice}, Amount: -5_000, Currency: "KZT"},
		{Ref: app.AccountRef{InternalCode: "cash_in_kzt"}, Amount: 5_000, Currency: "KZT"},
	}
	const n = 10
	ids := make(chan string, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			entry, err := svc.PostTransaction(ctx, "transfer", "race-1", "", specs)
			if err != nil {
				errs <- err
				return
			}
			ids <- entry.ID
		}()
	}
	var firstID string
	for i := 0; i < n; i++ {
		select {
		case err := <-errs:
			t.Fatal(err)
		case id := <-ids:
			if firstID == "" {
				firstID = id
			} else if id != firstID {
				t.Fatalf("two different entries created: %s vs %s", firstID, id)
			}
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM journal_entries WHERE reference_id = 'race-1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("want exactly 1 entry, got %d", count)
	}
	if b := balanceOf(t, svc, app.AccountRef{ExternalID: alice}); b.Balance != 95_000 {
		t.Fatalf("balance after concurrent replay: %d", b.Balance)
	}
}

// ADR-0017: rows land in the monthly partition, not the default one, and
// the DB zero-sum constraint trigger rejects raw unbalanced inserts.
func TestPartitionRoutingAndZeroSumTrigger(t *testing.T) {
	pool, _, svc := startLedgerDB(t)
	ctx := context.Background()
	alice := uuid.NewString()
	seedCustomer(t, svc, alice, 5_000)

	suffix := time.Now().UTC().Format("y2006m01")
	var inMonthly, inDefault int
	if err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM ONLY postings_%s`, suffix)).Scan(&inMonthly); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ONLY postings_default`).Scan(&inDefault); err != nil {
		t.Fatal(err)
	}
	if inMonthly == 0 || inDefault != 0 {
		t.Fatalf("partition routing broken: monthly=%d default=%d", inMonthly, inDefault)
	}

	// Bypass the app entirely: raw unbalanced entry must be rejected by the
	// deferred constraint trigger at commit (ADR-0006).
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := time.Now().UTC()
	entryID := uuid.NewString()
	var acctID string
	if err := pool.QueryRow(ctx, `SELECT id FROM ledger_accounts WHERE internal_code = 'cash_in_kzt'`).Scan(&acctID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO journal_entries (id, reference_type, reference_id, occurred_at) VALUES ($1,'hack','hack-1',$2)`,
		entryID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO postings (id, entry_id, account_id, amount, currency, occurred_at) VALUES ($1,$2,$3,-100,'KZT',$4),($5,$2,$3,99,'KZT',$4)`,
		uuid.NewString(), entryID, acctID, now, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("unbalanced raw insert committed — zero-sum trigger missing")
	}
}

func TestHoldLifecycleIntegration(t *testing.T) {
	_, _, svc := startLedgerDB(t)
	ctx := context.Background()
	alice := uuid.NewString()
	seedCustomer(t, svc, alice, 10_000)
	ref := app.AccountRef{ExternalID: alice}

	hold, err := svc.PlaceHold(ctx, ref, 4_000, "KZT", "transfer", "t-hold-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if b := balanceOf(t, svc, ref); b.Held != 4_000 || b.Balance != 10_000 {
		t.Fatalf("after hold: %+v", b)
	}

	// idempotent replay returns the same hold
	replay, err := svc.PlaceHold(ctx, ref, 4_000, "KZT", "transfer", "t-hold-1", 0)
	if err != nil || replay.ID != hold.ID {
		t.Fatalf("replay: %v %s vs %s", err, replay.ID, hold.ID)
	}

	// available = 6000: a 7000 hold must fail
	if _, err := svc.PlaceHold(ctx, ref, 7_000, "KZT", "transfer", "t-hold-2", 0); apperr.CodeOf(err) != apperr.CodeInsufficientFunds {
		t.Fatalf("overdraw hold: %v", err)
	}

	// capture via PostTransaction
	entry, err := svc.PostTransaction(ctx, "transfer", "t-hold-1", hold.ID, []app.PostingSpec{
		{Ref: ref, Amount: -4_000, Currency: "KZT"},
		{Ref: app.AccountRef{InternalCode: "cash_in_kzt"}, Amount: 4_000, Currency: "KZT"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entry.Postings) != 2 {
		t.Fatalf("postings: %d", len(entry.Postings))
	}
	if b := balanceOf(t, svc, ref); b.Balance != 6_000 || b.Held != 0 {
		t.Fatalf("after capture: %+v", b)
	}

	// releasing a captured hold must fail; releasing a fresh hold restores held
	if _, err := svc.ReleaseHold(ctx, hold.ID); apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("release captured: %v", err)
	}
	h2, err := svc.PlaceHold(ctx, ref, 1_000, "KZT", "transfer", "t-hold-3", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReleaseHold(ctx, h2.ID); err != nil {
		t.Fatal(err)
	}
	// release is idempotent
	if _, err := svc.ReleaseHold(ctx, h2.ID); err != nil {
		t.Fatal(err)
	}
	if b := balanceOf(t, svc, ref); b.Held != 0 {
		t.Fatalf("held not restored: %+v", b)
	}
}

func TestFrozenAccountRejected(t *testing.T) {
	pool, _, svc := startLedgerDB(t)
	ctx := context.Background()
	alice := uuid.NewString()
	seedCustomer(t, svc, alice, 10_000)
	if _, err := pool.Exec(ctx, `UPDATE ledger_accounts SET status='FROZEN' WHERE external_account_id=$1`, alice); err != nil {
		t.Fatal(err)
	}
	_, err := svc.PlaceHold(ctx, app.AccountRef{ExternalID: alice}, 100, "KZT", "transfer", "t-frozen", 0)
	if apperr.CodeOf(err) != apperr.CodeAccountFrozen {
		t.Fatalf("want ACCOUNT_FROZEN, got %v", err)
	}
	_, err = svc.PostTransaction(ctx, "transfer", "t-frozen-post", "", []app.PostingSpec{
		{Ref: app.AccountRef{ExternalID: alice}, Amount: -100, Currency: "KZT"},
		{Ref: app.AccountRef{InternalCode: "cash_in_kzt"}, Amount: 100, Currency: "KZT"},
	})
	if apperr.CodeOf(err) != apperr.CodeAccountFrozen {
		t.Fatalf("want ACCOUNT_FROZEN, got %v", err)
	}
}
