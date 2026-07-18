package money

import (
	"math"
	"testing"
)

func TestAddOverflow(t *testing.T) {
	if _, err := AddInt64(math.MaxInt64, 1); err == nil {
		t.Fatal("want overflow")
	}
	if _, err := AddInt64(math.MinInt64, -1); err == nil {
		t.Fatal("want overflow")
	}
	v, err := AddInt64(math.MaxInt64-1, 1)
	if err != nil || v != math.MaxInt64 {
		t.Fatalf("got %d, %v", v, err)
	}
}

func TestAddCurrencyMismatch(t *testing.T) {
	a := Amount{100, KZT}
	b := Amount{100, USD}
	if _, err := a.Add(b); err != ErrCurrencyMismatch {
		t.Fatalf("want ErrCurrencyMismatch, got %v", err)
	}
}

func TestNewRejectsUnknownCurrency(t *testing.T) {
	if _, err := New(1, "EUR"); err == nil {
		t.Fatal("want error for EUR")
	}
}

func TestNegMinInt64(t *testing.T) {
	a := Amount{math.MinInt64, KZT}
	if _, err := a.Neg(); err == nil {
		t.Fatal("want overflow on -MinInt64")
	}
}

// Banker's rounding cases: ties go to the even neighbour.
func TestConvertHalfEven(t *testing.T) {
	cases := []struct {
		name       string
		value      int64
		rateMicros int64
		want       int64
	}{
		// 1 × 0.5 = 0.5 → 0 (even)
		{"tie down to even", 1, 500_000, 0},
		// 3 × 0.5 = 1.5 → 2 (even)
		{"tie up to even", 3, 500_000, 2},
		// 5 × 0.5 = 2.5 → 2 (even)
		{"tie 2.5 to 2", 5, 500_000, 2},
		// 7 × 0.5 = 3.5 → 4 (even)
		{"tie 3.5 to 4", 7, 500_000, 4},
		// plain nearest, no tie: 100 × 4.7825 = 478.25 → 478
		{"nearest down", 100, 4_782_500, 478},
		// 100 × 4.7875 = 478.75 → 479
		{"nearest up", 100, 4_787_500, 479},
		// USD→KZT realistic: 10000 cents × 478.25 = 4_782_500 tiyn
		{"usd to kzt", 10_000, 478_250_000, 4_782_500},
		{"zero value", 0, 478_250_000, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Convert(c.value, c.rateMicros)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("Convert(%d, %d) = %d, want %d", c.value, c.rateMicros, got, c.want)
			}
		})
	}
}

func TestConvertInverse(t *testing.T) {
	// 482.75 KZT/USD, 96550 tiyn → 200 cents exactly.
	got, err := ConvertInverse(96_550, 482_750_000)
	if err != nil || got != 200 {
		t.Fatalf("got %d, %v", got, err)
	}
	if _, err := ConvertInverse(1, 0); err == nil {
		t.Fatal("want error on zero rate")
	}
}

func TestConvertOverflow(t *testing.T) {
	if _, err := Convert(math.MaxInt64, 2_000_000); err != ErrOverflow {
		t.Fatalf("want ErrOverflow, got %v", err)
	}
}

func TestFormatRate(t *testing.T) {
	if s := FormatRate(478_250_000); s != "478.250000" {
		t.Fatalf("got %s", s)
	}
	if s := FormatRate(1_000_001); s != "1.000001" {
		t.Fatalf("got %s", s)
	}
}
