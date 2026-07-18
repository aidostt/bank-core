package apperr

import "net/http"

// httpStatus: the single gateway-side mapping table (ADR-0018).
var httpStatus = map[Code]int{
	CodeInvalidArgument:        http.StatusBadRequest,
	CodeNotFound:               http.StatusNotFound,
	CodeAlreadyExists:          http.StatusConflict,
	CodeUnauthenticated:        http.StatusUnauthorized,
	CodeForbidden:              http.StatusForbidden,
	CodeInsufficientFunds:      http.StatusUnprocessableEntity,
	CodeAccountFrozen:          http.StatusUnprocessableEntity,
	CodeCurrencyMismatch:       http.StatusUnprocessableEntity,
	CodeUnbalancedEntry:        http.StatusUnprocessableEntity,
	CodeLimitExceeded:          http.StatusUnprocessableEntity,
	CodeIdempotencyConflict:    http.StatusUnprocessableEntity,
	CodeIdempotencyKeyRequired: http.StatusBadRequest,
	CodeRateLimited:            http.StatusTooManyRequests,
	CodeUpstreamUnavailable:    http.StatusServiceUnavailable,
	CodeInternal:               http.StatusInternalServerError,
}

var httpTitle = map[Code]string{
	CodeInvalidArgument:        "Invalid request",
	CodeNotFound:               "Not found",
	CodeAlreadyExists:          "Already exists",
	CodeUnauthenticated:        "Authentication required",
	CodeForbidden:              "Forbidden",
	CodeInsufficientFunds:      "Insufficient funds",
	CodeAccountFrozen:          "Account frozen",
	CodeCurrencyMismatch:       "Currency mismatch",
	CodeUnbalancedEntry:        "Unbalanced journal entry",
	CodeLimitExceeded:          "Limit exceeded",
	CodeIdempotencyConflict:    "Idempotency key conflict",
	CodeIdempotencyKeyRequired: "Idempotency-Key header required",
	CodeRateLimited:            "Too many requests",
	CodeUpstreamUnavailable:    "Service temporarily unavailable",
	CodeInternal:               "Internal error",
}

func HTTPStatus(c Code) int {
	if s, ok := httpStatus[c]; ok {
		return s
	}
	return http.StatusInternalServerError
}

func HTTPTitle(c Code) string {
	if t, ok := httpTitle[c]; ok {
		return t
	}
	return "Internal error"
}
