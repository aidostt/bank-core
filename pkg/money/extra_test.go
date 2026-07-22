package money

import (
	"math"
	"testing"
)

func TestAddSubNegString(t *testing.T) {
	a := Amount{Value: 100, Currency: KZT}
	b := Amount{Value: 40, Currency: KZT}

	sum, err := a.Add(b)
	if err != nil || sum.Value != 140 {
		t.Fatalf("Add: %d %v", sum.Value, err)
	}
	diff, err := a.Sub(b)
	if err != nil || diff.Value != 60 {
		t.Fatalf("Sub: %d %v", diff.Value, err)
	}
	neg, err := a.Neg()
	if err != nil || neg.Value != -100 {
		t.Fatalf("Neg: %d %v", neg.Value, err)
	}
	if !a.IsPositive() || (Amount{Value: -1, Currency: KZT}).IsPositive() {
		t.Fatal("IsPositive")
	}
	if a.String() != "100 KZT" {
		t.Fatalf("String = %q", a.String())
	}
}

func TestSubOverflowGuards(t *testing.T) {
	// Sub of MinInt64 would overflow when negated.
	a := Amount{Value: 0, Currency: KZT}
	if _, err := a.Sub(Amount{Value: math.MinInt64, Currency: KZT}); err != ErrOverflow {
		t.Fatalf("Sub MinInt64: %v", err)
	}
	// currency mismatch propagates through Sub.
	if _, err := a.Sub(Amount{Value: 1, Currency: USD}); err != ErrCurrencyMismatch {
		t.Fatalf("Sub mismatch: %v", err)
	}
}

func TestValidCurrency(t *testing.T) {
	if !ValidCurrency(KZT) || !ValidCurrency(USD) || ValidCurrency("EUR") {
		t.Fatal("ValidCurrency")
	}
}
