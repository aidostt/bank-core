// Package domain: account numbering and status rules. Pure — stdlib only
// (ADR-0014).
package domain

import (
	"errors"
	"strings"
)

var (
	ErrUnknownCurrency   = errors.New("unknown currency")
	ErrAccountNotFound   = errors.New("account not found")
	ErrNotOwner          = errors.New("account does not belong to caller")
	ErrIllegalTransition = errors.New("illegal account status transition")
	ErrBadRandomPart     = errors.New("random part must be 13 digits")
)

const (
	StatusActive = "ACTIVE"
	StatusFrozen = "FROZEN"
	StatusClosed = "CLOSED"
)

// bankCode is the fictional bank identifier inside the account number
// (position of a real KZ bank code in an IBAN).
const bankCode = "125"

// GenerateNumber builds a KZ IBAN-style number: "KZ" + 2 check digits
// (ISO 7064 mod 97-10) + 3-digit bank code + 13 random digits.
func GenerateNumber(random13 string) (string, error) {
	if len(random13) != 13 || strings.Trim(random13, "0123456789") != "" {
		return "", ErrBadRandomPart
	}
	bban := bankCode + random13
	// Check digits: move "KZ00" to the end, letters → numbers (K=20, Z=35),
	// check = 98 − (N mod 97).
	check := 98 - mod97(bban+"203500")
	return "KZ" + twoDigits(check) + bban, nil
}

// ValidNumber verifies the IBAN check digits.
func ValidNumber(number string) bool {
	if len(number) != 20 || !strings.HasPrefix(number, "KZ") {
		return false
	}
	rearranged := number[4:] + "2035" + number[2:4] // KZ → 20 35
	return mod97(rearranged) == 1
}

func mod97(digits string) int {
	rem := 0
	for _, ch := range digits {
		if ch < '0' || ch > '9' {
			return -1 // never valid
		}
		rem = (rem*10 + int(ch-'0')) % 97
	}
	return rem
}

func twoDigits(n int) string {
	return string([]byte{byte('0' + n/10), byte('0' + n%10)}) // #nosec G115 -- n ∈ [2,98] by mod-97 construction
}

// CanTransition encodes ACTIVE ⇄ FROZEN → CLOSED.
func CanTransition(from, to string) bool {
	switch from {
	case StatusActive:
		return to == StatusFrozen || to == StatusClosed
	case StatusFrozen:
		return to == StatusActive || to == StatusClosed
	default:
		return false
	}
}
