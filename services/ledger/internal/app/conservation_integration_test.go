//go:build integration

package app_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"

	"github.com/aidostt/bank-core/pkg/apperr"
	"github.com/google/uuid"

	"github.com/aidostt/bank-core/services/ledger/internal/app"
)

// The concurrency test from the ledger doc: 100 goroutines fire random
// transfers between 4 accounts (half through hold/capture); money must be
// conserved, no deadlocks, no negative available balances, and every
// materialized balance must equal Σ postings (invariant 4).
func TestConcurrentConservation(t *testing.T) {
	pool, store, svc := startLedgerDB(t)
	ctx := context.Background()

	const (
		accounts     = 4
		goroutines   = 100
		perGoroutine = 5
		seedAmount   = int64(1_000_000)
	)

	ids := make([]string, accounts)
	for i := range ids {
		ids[i] = uuid.NewString()
		seedCustomer(t, svc, ids[i], seedAmount)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, goroutines*perGoroutine)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(g), 42)) // #nosec G404 -- test randomness
			for i := 0; i < perGoroutine; i++ {
				from := ids[rng.IntN(accounts)]
				to := ids[rng.IntN(accounts)]
				if from == to {
					continue
				}
				amount := int64(1 + rng.IntN(5_000))
				refID := fmt.Sprintf("ct-%d-%d", g, i)
				specs := []app.PostingSpec{
					{Ref: app.AccountRef{ExternalID: from}, Amount: -amount, Currency: "KZT"},
					{Ref: app.AccountRef{ExternalID: to}, Amount: amount, Currency: "KZT"},
				}
				var err error
				if i%2 == 0 {
					// hold → capture path
					h, herr := svc.PlaceHold(ctx, app.AccountRef{ExternalID: from}, amount, "KZT", "transfer", refID, 0)
					if herr != nil {
						err = herr
					} else {
						_, err = svc.PostTransaction(ctx, "transfer", refID, h.ID, specs)
					}
				} else {
					_, err = svc.PostTransaction(ctx, "transfer", refID, "", specs)
				}
				if err != nil && apperr.CodeOf(err) != apperr.CodeInsufficientFunds {
					errCh <- fmt.Errorf("goroutine %d op %d: %w", g, i, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err) // deadlocks, lock timeouts, unexpected failures
	}
	if t.Failed() {
		t.FailNow()
	}

	// Conservation: customers hold exactly what cash_in emitted.
	var total int64
	for _, id := range ids {
		b := balanceOf(t, svc, app.AccountRef{ExternalID: id})
		if b.Balance < 0 {
			t.Errorf("customer balance negative: %s = %d", id, b.Balance)
		}
		if b.Held != 0 {
			t.Errorf("dangling held on %s: %d", id, b.Held)
		}
		if b.Balance-b.Held < 0 {
			t.Errorf("negative available on %s", id)
		}
		total += b.Balance
	}
	if total != accounts*seedAmount {
		t.Errorf("money not conserved: customers sum %d, want %d", total, accounts*seedAmount)
	}
	cash := balanceOf(t, svc, app.AccountRef{InternalCode: "cash_in_kzt"})
	if cash.Balance != -accounts*seedAmount {
		t.Errorf("cash_in mismatch: %d", cash.Balance)
	}

	// Invariant 4: materialized balance ≡ Σ postings, for every account.
	rows, err := pool.Query(ctx, `SELECT account_id FROM account_balances`)
	if err != nil {
		t.Fatal(err)
	}
	var allIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		allIDs = append(allIDs, id)
	}
	rows.Close()
	if errors.Is(rows.Err(), context.Canceled) || rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	for _, id := range allIDs {
		sum, err := store.SumPostingsForAccount(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		var materialized int64
		if err := pool.QueryRow(ctx, `SELECT balance FROM account_balances WHERE account_id=$1`, id).Scan(&materialized); err != nil {
			t.Fatal(err)
		}
		if sum != materialized {
			t.Errorf("invariant 4 violated on %s: Σpostings=%d materialized=%d", id, sum, materialized)
		}
	}

	// Reconciliation query used by make verify-ledger: zero-sum per entry.
	var badEntries int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM (
			SELECT entry_id FROM postings GROUP BY entry_id, currency HAVING sum(amount) <> 0
		) bad`).Scan(&badEntries); err != nil {
		t.Fatal(err)
	}
	if badEntries != 0 {
		t.Errorf("%d entries violate zero-sum", badEntries)
	}
}
