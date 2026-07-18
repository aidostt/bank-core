// Package apperr is the single error-model registry (ADR-0018): stable
// machine codes, one mapping to gRPC statuses with errdetails at service
// edges, one mapping to RFC 9457 problem+json at the gateway.
// Domain packages keep their own pure sentinel errors; adapters translate
// them into *apperr.Error exactly once.
package apperr

import (
	"errors"
	"fmt"
)

// Code is a stable machine-readable error code carried end-to-end:
// domain error → gRPC errdetails.ErrorInfo.Reason → problem+json "code".
type Code string

const (
	CodeInvalidArgument        Code = "INVALID_ARGUMENT"
	CodeNotFound               Code = "NOT_FOUND"
	CodeAlreadyExists          Code = "ALREADY_EXISTS"
	CodeUnauthenticated        Code = "UNAUTHENTICATED"
	CodeForbidden              Code = "FORBIDDEN"
	CodeInsufficientFunds      Code = "INSUFFICIENT_FUNDS"
	CodeAccountFrozen          Code = "ACCOUNT_FROZEN"
	CodeCurrencyMismatch       Code = "CURRENCY_MISMATCH"
	CodeUnbalancedEntry        Code = "UNBALANCED_ENTRY"
	CodeLimitExceeded          Code = "LIMIT_EXCEEDED"
	CodeIdempotencyConflict    Code = "IDEMPOTENCY_CONFLICT"
	CodeIdempotencyKeyRequired Code = "IDEMPOTENCY_KEY_REQUIRED"
	CodeRateLimited            Code = "RATE_LIMITED"
	CodeUpstreamUnavailable    Code = "UPSTREAM_UNAVAILABLE"
	CodeInternal               Code = "INTERNAL"
)

// Error is a typed application error with a stable code.
type Error struct {
	Code    Code
	Message string
	cause   error
}

func New(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

func Newf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func Wrap(code Code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, cause: cause}
}

func (e *Error) Error() string {
	if e.cause != nil {
		return e.Message + ": " + e.cause.Error()
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.cause }

// CodeOf extracts the stable code; CodeInternal for unknown errors.
func CodeOf(err error) Code {
	var ae *Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	return CodeInternal
}

// Retryable reports whether a client may safely retry (ADR-0018: only
// transport-level unavailability is client-retryable).
func Retryable(code Code) bool {
	return code == CodeUpstreamUnavailable
}
