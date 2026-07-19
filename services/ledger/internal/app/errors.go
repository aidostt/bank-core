package app

import (
	"errors"

	"github.com/aidostt/bank-core/pkg/apperr"

	"github.com/aidostt/bank-core/services/ledger/internal/domain"
)

// toAppErr translates pure domain errors into the typed error model exactly
// once (ADR-0018); the gRPC adapter only calls apperr.ToGRPC.
func toAppErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, domain.ErrInsufficientFunds):
		return apperr.Wrap(apperr.CodeInsufficientFunds, "insufficient available funds", err)
	case errors.Is(err, domain.ErrAccountFrozen):
		return apperr.Wrap(apperr.CodeAccountFrozen, "account is not active", err)
	case errors.Is(err, domain.ErrCurrencyMismatch), errors.Is(err, domain.ErrUnknownCurrency):
		return apperr.Wrap(apperr.CodeCurrencyMismatch, err.Error(), err)
	case errors.Is(err, domain.ErrUnbalancedEntry),
		errors.Is(err, domain.ErrTooFewPostings),
		errors.Is(err, domain.ErrZeroPosting),
		errors.Is(err, domain.ErrOverflow):
		return apperr.Wrap(apperr.CodeUnbalancedEntry, err.Error(), err)
	case errors.Is(err, domain.ErrDuplicateReference):
		return apperr.Wrap(apperr.CodeAlreadyExists, "reference already used with a different payload", err)
	case errors.Is(err, domain.ErrHoldNotActive),
		errors.Is(err, domain.ErrHoldExceeded),
		errors.Is(err, domain.ErrHoldAccountMismatch),
		errors.Is(err, domain.ErrInvalidHoldAmount):
		return apperr.Wrap(apperr.CodeInvalidArgument, err.Error(), err)
	case errors.Is(err, domain.ErrAccountNotFound), errors.Is(err, ErrNotFound):
		return apperr.Wrap(apperr.CodeNotFound, "not found", err)
	default:
		return err
	}
}
