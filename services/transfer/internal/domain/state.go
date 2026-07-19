// Package domain: the pure transfer saga state machine (ADR-0010). The
// transition function is total and exhaustively unit-tested; illegal
// transitions return ErrIllegalTransition. Stdlib only (ADR-0014).
package domain

import "errors"

var (
	ErrIllegalTransition = errors.New("illegal state transition")
	ErrInvalidSpec       = errors.New("invalid transfer specification")
	ErrSameAccount       = errors.New("source and destination are the same account")
	ErrLimitExceeded     = errors.New("limit exceeded")
	ErrAmountTooSmall    = errors.New("amount too small to convert")
)

type State string

const (
	StateCreated    State = "CREATED"
	StateValidating State = "VALIDATING"
	StateHeld       State = "HELD"
	StatePosting    State = "POSTING"
	StateCompleted  State = "COMPLETED"
	StateReleasing  State = "RELEASING"
	StateFailed     State = "FAILED"
)

// AllStates in a stable order (used by the exhaustive test).
var AllStates = []State{
	StateCreated, StateValidating, StateHeld, StatePosting,
	StateCompleted, StateReleasing, StateFailed,
}

type Event string

const (
	// EvStart begins processing a freshly persisted transfer.
	EvStart Event = "START"
	// EvValidated: accounts, limits and FX are locked in; the HELD state is
	// persisted BEFORE calling the ledger — PlaceHold is idempotent by
	// transfer id, so a crash between persist and call is recoverable.
	EvValidated        Event = "VALIDATED"
	EvValidationFailed Event = "VALIDATION_FAILED"
	// EvHoldFailed: the ledger definitively refused the hold.
	EvHoldFailed Event = "HOLD_FAILED"
	// EvPostingStarted: hold confirmed; the POSTING intent is persisted
	// before PostTransaction for the same crash-safety reason.
	EvPostingStarted Event = "POSTING_STARTED"
	EvPosted         Event = "POSTED"
	// EvPostFailed: the ledger definitively refused the posting.
	EvPostFailed Event = "POST_FAILED"
	// EvRecoveryExhausted: the recovery worker spent its retry budget.
	EvRecoveryExhausted Event = "RECOVERY_EXHAUSTED"
	EvReleased          Event = "RELEASED"
)

var AllEvents = []Event{
	EvStart, EvValidated, EvValidationFailed, EvHoldFailed,
	EvPostingStarted, EvPosted, EvPostFailed, EvRecoveryExhausted, EvReleased,
}

// transitions is the entire saga (architecture §4):
//
//	CREATED → VALIDATING → HELD → POSTING → COMPLETED
//	failure edges: VALIDATING→FAILED, HELD→RELEASING→FAILED,
//	POSTING→RELEASING (definitive) / recovery resolves ambiguity.
var transitions = map[State]map[Event]State{
	StateCreated: {
		EvStart: StateValidating,
	},
	StateValidating: {
		EvValidated:        StateHeld,
		EvValidationFailed: StateFailed,
	},
	StateHeld: {
		EvPostingStarted:    StatePosting,
		EvHoldFailed:        StateReleasing,
		EvRecoveryExhausted: StateReleasing,
	},
	StatePosting: {
		EvPosted:            StateCompleted,
		EvPostFailed:        StateReleasing,
		EvRecoveryExhausted: StateReleasing,
	},
	StateReleasing: {
		EvReleased: StateFailed,
	},
}

// Next is the pure transition function.
func Next(s State, e Event) (State, error) {
	if to, ok := transitions[s][e]; ok {
		return to, nil
	}
	return "", ErrIllegalTransition
}

func IsTerminal(s State) bool {
	return s == StateCompleted || s == StateFailed
}

type Type string

const (
	TypeTopup    Type = "TOPUP"
	TypeInternal Type = "INTERNAL"
	TypeP2P      Type = "P2P"
)

// ValidateSpec checks the structural shape of a create request.
func ValidateSpec(t Type, fromAccountID, toAccountID, toAccountNumber string, amount int64, currency string) error {
	if amount <= 0 {
		return ErrInvalidSpec
	}
	if currency != "KZT" && currency != "USD" {
		return ErrInvalidSpec
	}
	switch t {
	case TypeTopup:
		if toAccountID == "" || fromAccountID != "" {
			return ErrInvalidSpec
		}
	case TypeInternal:
		if fromAccountID == "" || toAccountID == "" {
			return ErrInvalidSpec
		}
		if fromAccountID == toAccountID {
			return ErrSameAccount
		}
	case TypeP2P:
		if fromAccountID == "" || toAccountNumber == "" {
			return ErrInvalidSpec
		}
	default:
		return ErrInvalidSpec
	}
	return nil
}

// CheckLimits enforces per-transaction and daily caps (transfer doc).
func CheckLimits(amount, perTx, dailyUsed, dailyLimit int64) error {
	if amount > perTx {
		return ErrLimitExceeded
	}
	if dailyUsed > dailyLimit-amount {
		return ErrLimitExceeded
	}
	return nil
}
