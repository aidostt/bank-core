//go:build integration

package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/aidostt/bank-core/pkg/grpcx"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/google/uuid"

	"github.com/aidostt/bank-core/services/transfer/internal/app"
	"github.com/aidostt/bank-core/services/transfer/internal/domain"
)

// M1 DoD: kill the ledger between hold and post — the transfer must not
// stay stuck. Scenario A: the ledger actually applied the entry before the
// timeout; recovery finds it via GetTransactionByReference and completes.
func TestRecoveryCompletesWhenEntryExists(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice, bob := uuid.NewString(), uuid.NewString()
	src := f.account.add(alice, "KZT", "ACTIVE")
	dst := f.account.add(bob, "KZT", "ACTIVE")
	f.ledger.balances[src.Id] = 1_000_000
	f.ledger.failPostTimes = 100 // ledger "dies" right after PlaceHold

	tr, err := f.svc.CreateTransfer(ctx, app.CreateCmd{
		CustomerID: alice, IdempotencyKey: "rec-a", Type: domain.TypeP2P,
		FromAccountID: src.Id, ToAccountNumber: dst.Number,
		Amount: 5_000, Currency: "KZT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tr.State != domain.StatePosting {
		t.Fatalf("expected parked POSTING, got %s", tr.State)
	}

	// The entry actually landed on the ledger despite the timeout.
	f.ledger.mu.Lock()
	f.ledger.entryOnRecovery = true
	f.ledger.mu.Unlock()

	rec := app.NewRecovery(f.svc, time.Second, time.Millisecond, logging.New("test"))
	time.Sleep(50 * time.Millisecond) // let updated_at become stale
	rec.RunOnce(ctx)

	got, err := f.svc.Drive(ctx, tr.ID) // terminal → returns as-is
	if err != nil {
		t.Fatal(err)
	}
	if got.State != domain.StateCompleted {
		t.Fatalf("recovery did not complete: %s (reason %v)", got.State, got.Reason)
	}
}

// Scenario B: the entry never landed; the ledger comes back and recovery
// re-sends PostTransaction (idempotent by transfer id) to completion.
func TestRecoveryRepostsWhenLedgerReturns(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice, bob := uuid.NewString(), uuid.NewString()
	src := f.account.add(alice, "KZT", "ACTIVE")
	dst := f.account.add(bob, "KZT", "ACTIVE")
	f.ledger.balances[src.Id] = 1_000_000
	f.ledger.failPostTimes = 3 // exactly the synchronous retry budget

	tr, err := f.svc.CreateTransfer(ctx, app.CreateCmd{
		CustomerID: alice, IdempotencyKey: "rec-b", Type: domain.TypeP2P,
		FromAccountID: src.Id, ToAccountNumber: dst.Number,
		Amount: 7_000, Currency: "KZT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tr.State != domain.StatePosting {
		t.Fatalf("expected parked POSTING, got %s", tr.State)
	}

	rec := app.NewRecovery(f.svc, time.Second, time.Millisecond, logging.New("test"))
	time.Sleep(50 * time.Millisecond)
	rec.RunOnce(ctx)

	got, err := f.svc.Drive(ctx, tr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != domain.StateCompleted {
		t.Fatalf("recovery did not complete: %s (reason %v)", got.State, got.Reason)
	}
	if f.ledger.balances[dst.Id] != 7_000 {
		t.Fatalf("destination credited %d, want 7000 (exactly once)", f.ledger.balances[dst.Id])
	}
}

// Scenario C: the ledger never recovers within the retry budget — the
// transfer fails with reason=recovery_exhausted and the hold is released.
func TestRecoveryExhaustsAndReleases(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice, bob := uuid.NewString(), uuid.NewString()
	src := f.account.add(alice, "KZT", "ACTIVE")
	dst := f.account.add(bob, "KZT", "ACTIVE")
	f.ledger.balances[src.Id] = 1_000_000
	f.ledger.failPostTimes = 1_000_000 // ledger posting is down for good

	tr, err := f.svc.CreateTransfer(ctx, app.CreateCmd{
		CustomerID: alice, IdempotencyKey: "rec-c", Type: domain.TypeP2P,
		FromAccountID: src.Id, ToAccountNumber: dst.Number,
		Amount: 9_000, Currency: "KZT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tr.State != domain.StatePosting {
		t.Fatalf("expected parked POSTING, got %s", tr.State)
	}

	rec := app.NewRecovery(f.svc, time.Second, time.Millisecond, logging.New("test"))
	owner := grpcx.Claims{CustomerID: alice, Roles: []string{"customer"}}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		time.Sleep(20 * time.Millisecond)
		rec.RunOnce(ctx)
		got, err := f.svc.GetTransfer(ctx, owner, tr.ID)
		_ = err
		if got != nil && domain.IsTerminal(got.State) {
			if got.State != domain.StateFailed || got.Reason == nil || *got.Reason != "recovery_exhausted" {
				t.Fatalf("state=%s reason=%v", got.State, got.Reason)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("transfer never reached a terminal state")
		}
	}
	f.ledger.mu.Lock()
	releases := f.ledger.releaseCalls
	f.ledger.mu.Unlock()
	if releases == 0 {
		t.Fatal("hold was never released after exhaustion")
	}
}
