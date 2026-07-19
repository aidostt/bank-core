// Package domain holds the pure double-entry rules (ADR-0006). Every
// invariant in docs/services/ledger-service.md is enforced here and has a
// dedicated test. Stdlib only (ADR-0014, depguard).
package domain

import (
	"errors"
	"time"
)

var (
	ErrInsufficientFunds   = errors.New("insufficient available funds")
	ErrAccountFrozen       = errors.New("account is not active")
	ErrAccountNotFound     = errors.New("ledger account not found")
	ErrCurrencyMismatch    = errors.New("posting currency does not match account currency")
	ErrUnknownCurrency     = errors.New("unknown currency")
	ErrUnbalancedEntry     = errors.New("entry does not sum to zero per currency")
	ErrTooFewPostings      = errors.New("journal entry requires at least 2 postings")
	ErrZeroPosting         = errors.New("posting amount must be non-zero")
	ErrOverflow            = errors.New("amount overflow")
	ErrDuplicateReference  = errors.New("reference already used with a different payload")
	ErrHoldNotActive       = errors.New("hold is not active")
	ErrHoldExceeded        = errors.New("capture exceeds held amount")
	ErrHoldAccountMismatch = errors.New("hold belongs to a different account")
	ErrInvalidHoldAmount   = errors.New("hold amount must be positive")
)

const (
	TypeCustomer = "customer"
	TypeInternal = "internal"

	StatusActive = "ACTIVE"

	HoldActive   = "active"
	HoldCaptured = "captured"
	HoldReleased = "released"
)

type Account struct {
	ID           string
	ExternalID   string
	InternalCode string
	Type         string
	Currency     string
	Status       string
}

// Posting is one leg of a journal entry; Amount is signed minor units
// (negative = debit, positive = credit).
type Posting struct {
	AccountID string
	Amount    int64
	Currency  string
}

type Balance struct {
	AccountID string
	Currency  string
	Balance   int64
	Held      int64
	Version   int64
}

func (b Balance) Available() int64 { return b.Balance - b.Held }

type Hold struct {
	ID            string
	AccountID     string
	Amount        int64
	Currency      string
	ReferenceType string
	ReferenceID   string
	Status        string
	ExpiresAt     time.Time
}

func validCurrency(c string) bool { return c == "KZT" || c == "USD" }

func addChecked(a, b int64) (int64, error) {
	sum := a + b
	if (b > 0 && sum < a) || (b < 0 && sum > a) {
		return 0, ErrOverflow
	}
	return sum, nil
}

// ValidateEntry enforces invariant 1 (Σ postings = 0 per currency) plus
// structural rules: ≥2 legs, non-zero legs, currency matches the account,
// customer accounts must be ACTIVE (internal accounts always post — they
// absorb float, invariant 3 note).
func ValidateEntry(postings []Posting, accounts map[string]Account) error {
	if len(postings) < 2 {
		return ErrTooFewPostings
	}
	sums := map[string]int64{}
	for _, p := range postings {
		acct, ok := accounts[p.AccountID]
		if !ok {
			return ErrAccountNotFound
		}
		if p.Amount == 0 {
			return ErrZeroPosting
		}
		if !validCurrency(p.Currency) {
			return ErrUnknownCurrency
		}
		if p.Currency != acct.Currency {
			return ErrCurrencyMismatch
		}
		if acct.Type == TypeCustomer && acct.Status != StatusActive {
			return ErrAccountFrozen
		}
		s, err := addChecked(sums[p.Currency], p.Amount)
		if err != nil {
			return err
		}
		sums[p.Currency] = s
	}
	for _, s := range sums {
		if s != 0 {
			return ErrUnbalancedEntry
		}
	}
	return nil
}

// ApplyPosting computes the balance after one net delta. Invariant 3: for
// customer accounts, balance − held must stay ≥ 0; invariant 5: version
// increases by exactly 1 per balance change.
func ApplyPosting(b Balance, accountType string, delta int64) (Balance, error) {
	nb, err := addChecked(b.Balance, delta)
	if err != nil {
		return Balance{}, err
	}
	b.Balance = nb
	b.Version++
	if accountType == TypeCustomer && b.Available() < 0 {
		return Balance{}, ErrInsufficientFunds
	}
	return b, nil
}

// CanPlaceHold checks that reserving amount keeps the customer account
// non-negative (balance − held − amount ≥ 0). Internal accounts may go
// negative and skip the check.
func CanPlaceHold(b Balance, accountType string, amount int64) error {
	if amount <= 0 {
		return ErrInvalidHoldAmount
	}
	if accountType != TypeCustomer {
		return nil
	}
	if b.Available() < amount {
		return ErrInsufficientFunds
	}
	return nil
}

// CaptureHold transitions active → captured exactly once (invariant 7) and
// verifies the captured debit does not exceed the held amount.
func CaptureHold(h Hold, debit int64) (Hold, error) {
	if h.Status != HoldActive {
		return Hold{}, ErrHoldNotActive
	}
	if debit < 0 {
		debit = -debit
	}
	if debit > h.Amount {
		return Hold{}, ErrHoldExceeded
	}
	h.Status = HoldCaptured
	return h, nil
}

// ReleaseHold transitions active → released; releasing a released hold is
// a no-op (idempotent). A captured hold can never be released.
func ReleaseHold(h Hold) (Hold, bool, error) {
	switch h.Status {
	case HoldReleased:
		return h, false, nil
	case HoldCaptured:
		return Hold{}, false, ErrHoldNotActive
	default:
		h.Status = HoldReleased
		return h, true, nil
	}
}
