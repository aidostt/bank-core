package domain

import (
	"math"
	"testing"
)

var accounts = map[string]Account{
	"cust-kzt":  {ID: "cust-kzt", Type: TypeCustomer, Currency: "KZT", Status: StatusActive},
	"cust-usd":  {ID: "cust-usd", Type: TypeCustomer, Currency: "USD", Status: StatusActive},
	"frozen":    {ID: "frozen", Type: TypeCustomer, Currency: "KZT", Status: "FROZEN"},
	"cash-kzt":  {ID: "cash-kzt", Type: TypeInternal, Currency: "KZT", Status: StatusActive},
	"fx-kzt":    {ID: "fx-kzt", Type: TypeInternal, Currency: "KZT", Status: StatusActive},
	"fx-usd":    {ID: "fx-usd", Type: TypeInternal, Currency: "USD", Status: StatusActive},
	"frozen-in": {ID: "frozen-in", Type: TypeInternal, Currency: "KZT", Status: "FROZEN"},
}

// Invariant 1: Σ postings = 0 per currency.
func TestValidateEntryZeroSum(t *testing.T) {
	cases := []struct {
		name     string
		postings []Posting
		wantErr  error
	}{
		{"balanced 2-leg", []Posting{
			{"cash-kzt", -100, "KZT"}, {"cust-kzt", 100, "KZT"},
		}, nil},
		{"balanced 4-leg FX", []Posting{
			{"cust-usd", -1000, "USD"}, {"fx-usd", 1000, "USD"},
			{"fx-kzt", -478250, "KZT"}, {"cust-kzt", 478250, "KZT"},
		}, nil},
		{"unbalanced same currency", []Posting{
			{"cash-kzt", -100, "KZT"}, {"cust-kzt", 99, "KZT"},
		}, ErrUnbalancedEntry},
		{"unbalanced across currencies", []Posting{
			{"cust-usd", -1000, "USD"}, {"cust-kzt", 1000, "KZT"},
		}, ErrUnbalancedEntry},
		{"single leg", []Posting{{"cust-kzt", 0, "KZT"}}, ErrTooFewPostings},
		{"zero amount leg", []Posting{
			{"cash-kzt", 0, "KZT"}, {"cust-kzt", 0, "KZT"},
		}, ErrZeroPosting},
		{"currency mismatch vs account", []Posting{
			{"cust-usd", -100, "KZT"}, {"cust-kzt", 100, "KZT"},
		}, ErrCurrencyMismatch},
		{"unknown account", []Posting{
			{"nope", -100, "KZT"}, {"cust-kzt", 100, "KZT"},
		}, ErrAccountNotFound},
		{"frozen customer account", []Posting{
			{"frozen", -100, "KZT"}, {"cust-kzt", 100, "KZT"},
		}, ErrAccountFrozen},
		{"frozen internal account still posts", []Posting{
			{"frozen-in", -100, "KZT"}, {"cust-kzt", 100, "KZT"},
		}, nil},
		{"unknown currency", []Posting{
			{"cust-kzt", -1, "EUR"}, {"cash-kzt", 1, "EUR"},
		}, ErrUnknownCurrency},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := ValidateEntry(c.postings, accounts); err != c.wantErr {
				t.Fatalf("got %v, want %v", err, c.wantErr)
			}
		})
	}
}

// Invariant 3: customer balance − held ≥ 0; internal accounts may go negative.
func TestApplyPosting(t *testing.T) {
	b := Balance{Balance: 1000, Held: 300, Version: 7}

	nb, err := ApplyPosting(b, TypeCustomer, -700)
	if err != nil {
		t.Fatal(err)
	}
	if nb.Balance != 300 || nb.Version != 8 {
		t.Fatalf("balance %d version %d", nb.Balance, nb.Version)
	}

	// -701 would leave available = -1
	if _, err := ApplyPosting(b, TypeCustomer, -701); err != ErrInsufficientFunds {
		t.Fatalf("want ErrInsufficientFunds, got %v", err)
	}
	// internal account absorbs float
	if _, err := ApplyPosting(b, TypeInternal, -100000); err != nil {
		t.Fatalf("internal negative: %v", err)
	}
	// overflow guarded
	if _, err := ApplyPosting(Balance{Balance: math.MaxInt64}, TypeInternal, 1); err != ErrOverflow {
		t.Fatalf("want overflow, got %v", err)
	}
}

// Invariant 5: version increments by exactly 1 per change.
func TestVersionMonotonic(t *testing.T) {
	b := Balance{Version: 0}
	for i := 1; i <= 5; i++ {
		var err error
		b, err = ApplyPosting(b, TypeInternal, 10)
		if err != nil {
			t.Fatal(err)
		}
		if b.Version != int64(i) {
			t.Fatalf("version %d after %d changes", b.Version, i)
		}
	}
}

func TestCanPlaceHold(t *testing.T) {
	b := Balance{Balance: 1000, Held: 400}
	if err := CanPlaceHold(b, TypeCustomer, 600); err != nil {
		t.Fatal(err)
	}
	if err := CanPlaceHold(b, TypeCustomer, 601); err != ErrInsufficientFunds {
		t.Fatalf("want ErrInsufficientFunds, got %v", err)
	}
	if err := CanPlaceHold(b, TypeInternal, 10_000_000); err != nil {
		t.Fatalf("internal hold: %v", err)
	}
	if err := CanPlaceHold(b, TypeCustomer, 0); err != ErrInvalidHoldAmount {
		t.Fatalf("zero hold: %v", err)
	}
	if err := CanPlaceHold(b, TypeCustomer, -5); err != ErrInvalidHoldAmount {
		t.Fatalf("negative hold: %v", err)
	}
}

// Invariant 7: capture at most the held amount, exactly one transition.
func TestHoldLifecycle(t *testing.T) {
	h := Hold{ID: "h1", Amount: 500, Status: HoldActive}

	if _, err := CaptureHold(h, 501); err != ErrHoldExceeded {
		t.Fatalf("want ErrHoldExceeded, got %v", err)
	}
	captured, err := CaptureHold(h, 500)
	if err != nil || captured.Status != HoldCaptured {
		t.Fatalf("capture: %v", err)
	}
	// captured hold cannot be captured again
	if _, err := CaptureHold(captured, 1); err != ErrHoldNotActive {
		t.Fatalf("double capture: %v", err)
	}
	// captured hold cannot be released
	if _, _, err := ReleaseHold(captured); err != ErrHoldNotActive {
		t.Fatalf("release captured: %v", err)
	}

	released, changed, err := ReleaseHold(h)
	if err != nil || !changed || released.Status != HoldReleased {
		t.Fatalf("release: %v changed=%v", err, changed)
	}
	// releasing again is a no-op (idempotent)
	again, changed, err := ReleaseHold(released)
	if err != nil || changed || again.Status != HoldReleased {
		t.Fatalf("re-release: %v changed=%v", err, changed)
	}
}
