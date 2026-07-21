package domain

import (
	"testing"
	"time"
)

func testRules() []Rule {
	return []Rule{
		{ID: "R1", Kind: KindAmountOver, Severity: SeverityMedium,
			Thresholds: map[string]int64{"KZT": 10_000_000, "USD": 100_000}},
		{ID: "R2", Kind: KindVelocity, Severity: SeverityHigh, MaxIn5m: 10},
		{ID: "R3", Kind: KindDailyOutSum, Severity: SeverityHigh,
			Thresholds: map[string]int64{"KZT": 200_000_000, "USD": 500_000}},
		{ID: "R4", Kind: KindNewBeneficiary, Severity: SeverityMedium,
			Thresholds: map[string]int64{"KZT": 5_000_000, "USD": 50_000}},
	}
}

// Table-driven boundary tests (anti-fraud doc DoD).
func TestEvaluateBoundaries(t *testing.T) {
	cases := []struct {
		name string
		obs  Observation
		want []string // rule ids expected to fire
	}{
		{"all quiet", Observation{Amount: 100, Currency: "KZT", Count5m: 1, DayOutSum: 100}, nil},
		{"R1 exactly at threshold does not fire", Observation{Amount: 10_000_000, Currency: "KZT", Count5m: 1, DayOutSum: 10_000_000}, nil},
		{"R1 one over threshold fires", Observation{Amount: 10_000_001, Currency: "KZT", Count5m: 1, DayOutSum: 10_000_001}, []string{"R1"}},
		{"R1 USD threshold independent", Observation{Amount: 100_001, Currency: "USD", Count5m: 1, DayOutSum: 100_001}, []string{"R1"}},
		{"R2 at limit does not fire", Observation{Amount: 1, Currency: "KZT", Count5m: 10, DayOutSum: 1}, nil},
		{"R2 over limit fires HIGH", Observation{Amount: 1, Currency: "KZT", Count5m: 11, DayOutSum: 1}, []string{"R2"}},
		{"R3 over daily sum fires HIGH", Observation{Amount: 1, Currency: "KZT", Count5m: 1, DayOutSum: 200_000_001}, []string{"R3"}},
		{"R4 new beneficiary small amount quiet", Observation{Amount: 5_000_000, Currency: "KZT", Count5m: 1, DayOutSum: 5_000_000, NewBeneficiary: true}, nil},
		{"R4 new beneficiary large fires", Observation{Amount: 5_000_001, Currency: "KZT", Count5m: 1, DayOutSum: 5_000_001, NewBeneficiary: true}, []string{"R4"}},
		{"R4 known beneficiary large quiet", Observation{Amount: 5_000_001, Currency: "KZT", Count5m: 1, DayOutSum: 5_000_001, NewBeneficiary: false}, []string{"R4-not"}},
		{"multiple rules fire together", Observation{Amount: 10_000_001, Currency: "KZT", Count5m: 11, DayOutSum: 200_000_001, NewBeneficiary: true}, []string{"R1", "R2", "R3", "R4"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Evaluate(testRules(), c.obs)
			var ids []string
			for _, a := range got {
				ids = append(ids, a.RuleID)
			}
			want := c.want
			if len(want) == 1 && want[0] == "R4-not" {
				want = nil // sentinel for the known-beneficiary negative case
			}
			if len(ids) != len(want) {
				t.Fatalf("fired %v, want %v", ids, want)
			}
			for i := range want {
				if ids[i] != want[i] {
					t.Fatalf("fired %v, want %v", ids, want)
				}
			}
		})
	}
}

func TestEvaluateSeverities(t *testing.T) {
	obs := Observation{Amount: 10_000_001, Currency: "KZT", Count5m: 11, DayOutSum: 200_000_001, NewBeneficiary: true}
	bySeverity := map[string]int{}
	for _, a := range Evaluate(testRules(), obs) {
		bySeverity[a.Severity]++
	}
	if bySeverity[SeverityHigh] != 2 || bySeverity[SeverityMedium] != 2 {
		t.Fatalf("severities: %v", bySeverity)
	}
}

func TestAdvanceWindow(t *testing.T) {
	start := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

	// inside window: counter grows
	w, n := AdvanceWindow(start, 3, start.Add(4*time.Minute+59*time.Second))
	if !w.Equal(start) || n != 4 {
		t.Fatalf("inside: %v %d", w, n)
	}
	// at exactly 5m the window slides
	w, n = AdvanceWindow(start, 3, start.Add(5*time.Minute))
	if !w.Equal(start.Add(5*time.Minute)) || n != 1 {
		t.Fatalf("slide: %v %d", w, n)
	}
	// zero start bootstraps
	w, n = AdvanceWindow(time.Time{}, 0, start)
	if !w.Equal(start) || n != 1 {
		t.Fatalf("bootstrap: %v %d", w, n)
	}
}

func TestAdvanceDay(t *testing.T) {
	at := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	day, sum := AdvanceDay(at, 100, at.Add(2*time.Hour), 50)
	if sum != 150 {
		t.Fatalf("same day sum: %d", sum)
	}
	day2, sum2 := AdvanceDay(day, sum, at.Add(24*time.Hour), 70)
	if sum2 != 70 || day2.Equal(day) {
		t.Fatalf("rollover: %v %d", day2, sum2)
	}
}
