// Package money implements integer minor-unit amounts (tiyn/cents) with
// overflow-checked arithmetic and banker's rounding for FX conversion.
// Floats for money are forbidden everywhere in bank-core (project conventions).
package money

import (
	"errors"
	"fmt"
)

const (
	KZT = "KZT"
	USD = "USD"
)

var (
	ErrCurrencyMismatch = errors.New("currency mismatch")
	ErrUnknownCurrency  = errors.New("unknown currency")
	ErrOverflow         = errors.New("amount overflow")
	ErrInvalidAmount    = errors.New("invalid amount")
)

// Amount is a monetary value in integer minor units of Currency.
type Amount struct {
	Value    int64
	Currency string
}

func ValidCurrency(c string) bool {
	return c == KZT || c == USD
}

func New(value int64, currency string) (Amount, error) {
	if !ValidCurrency(currency) {
		return Amount{}, fmt.Errorf("%w: %q", ErrUnknownCurrency, currency)
	}
	return Amount{Value: value, Currency: currency}, nil
}

// AddInt64 returns a+b with overflow detection.
func AddInt64(a, b int64) (int64, error) {
	sum := a + b
	if (b > 0 && sum < a) || (b < 0 && sum > a) {
		return 0, ErrOverflow
	}
	return sum, nil
}

func (a Amount) Add(b Amount) (Amount, error) {
	if a.Currency != b.Currency {
		return Amount{}, ErrCurrencyMismatch
	}
	v, err := AddInt64(a.Value, b.Value)
	if err != nil {
		return Amount{}, err
	}
	return Amount{Value: v, Currency: a.Currency}, nil
}

func (a Amount) Sub(b Amount) (Amount, error) {
	if b.Value == minInt64 {
		return Amount{}, ErrOverflow
	}
	return a.Add(Amount{Value: -b.Value, Currency: b.Currency})
}

func (a Amount) Neg() (Amount, error) {
	if a.Value == minInt64 {
		return Amount{}, ErrOverflow
	}
	return Amount{Value: -a.Value, Currency: a.Currency}, nil
}

func (a Amount) IsPositive() bool { return a.Value > 0 }

func (a Amount) String() string {
	return fmt.Sprintf("%d %s", a.Value, a.Currency)
}

const minInt64 = -1 << 63
