// Package domain: pure fraud-rule evaluation (anti-fraud doc). Stdlib only
// (ADR-0014); rule params arrive parsed, the evaluator is a total function
// over the observed transfer + the customer's stats.
package domain

import "time"

const (
	SeverityMedium = "MEDIUM"
	SeverityHigh   = "HIGH"

	KindAmountOver     = "amount_over"
	KindVelocity       = "velocity"
	KindDailyOutSum    = "daily_out_sum"
	KindNewBeneficiary = "new_beneficiary"
)

// Rule is one enabled row of the rules table with parsed params.
type Rule struct {
	ID       string
	Kind     string
	Severity string
	// Thresholds per currency (amount_over, new_beneficiary, daily_out_sum).
	Thresholds map[string]int64
	// MaxIn5m for velocity.
	MaxIn5m int
}

// Observation is the transfer fact plus the customer's updated stats at
// evaluation time.
type Observation struct {
	CustomerID     string
	TransferID     string
	AccountID      string // source account (freeze target)
	Amount         int64
	Currency       string
	At             time.Time
	Count5m        int   // transfers within the current 5m window, incl. this one
	DayOutSum      int64 // total outgoing today in Currency, incl. this one
	NewBeneficiary bool  // destination never seen for this customer before
}

// Alert is a raised finding.
type Alert struct {
	RuleID   string
	Severity string
	Detail   string
}

// Evaluate runs every enabled rule against the observation. Deterministic,
// order-stable (rule slice order), boundary semantics: strictly greater
// than thresholds trigger (doc: "amount > threshold", ">N transfers").
func Evaluate(rules []Rule, obs Observation) []Alert {
	var alerts []Alert
	for _, r := range rules {
		switch r.Kind {
		case KindAmountOver:
			if limit, ok := r.Thresholds[obs.Currency]; ok && obs.Amount > limit {
				alerts = append(alerts, Alert{r.ID, r.Severity, "amount over threshold"})
			}
		case KindVelocity:
			if r.MaxIn5m > 0 && obs.Count5m > r.MaxIn5m {
				alerts = append(alerts, Alert{r.ID, r.Severity, "transfer velocity exceeded"})
			}
		case KindDailyOutSum:
			if limit, ok := r.Thresholds[obs.Currency]; ok && obs.DayOutSum > limit {
				alerts = append(alerts, Alert{r.ID, r.Severity, "daily outgoing sum exceeded"})
			}
		case KindNewBeneficiary:
			if limit, ok := r.Thresholds[obs.Currency]; ok && obs.NewBeneficiary && obs.Amount > limit {
				alerts = append(alerts, Alert{r.ID, r.Severity, "large transfer to new beneficiary"})
			}
		}
	}
	return alerts
}

// AdvanceWindow slides the 5-minute window: if the observation falls outside
// the current window the counter restarts at 1, otherwise it increments.
func AdvanceWindow(winStart time.Time, count int, at time.Time) (time.Time, int) {
	if at.Sub(winStart) >= 5*time.Minute || winStart.IsZero() {
		return at, 1
	}
	return winStart, count + 1
}

// AdvanceDay resets the daily sum on date change (UTC).
func AdvanceDay(day time.Time, sum int64, at time.Time, amount int64) (time.Time, int64) {
	dayUTC := at.UTC().Truncate(24 * time.Hour)
	if !dayUTC.Equal(day.UTC().Truncate(24 * time.Hour)) {
		return dayUTC, amount
	}
	return dayUTC, sum + amount
}
