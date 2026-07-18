# ADR-0018: Typed domain errors, problem+json at the edge

Status: accepted · Date: 2026-07-18

## Decision
Domain packages define sentinel/typed errors (ErrInsufficientFunds,
ErrAccountFrozen, ErrLimitExceeded, ErrDuplicate, ErrNotFound, ErrForbidden…).
Adapters map them exactly once: to gRPC status codes with `errdetails` (service
edge) and to RFC 9457 problem+json with stable machine `code` fields (gateway).
Retryability is encoded in the mapping (only UNAVAILABLE/DEADLINE_EXCEEDED are
client-retryable). Internal errors never leak details to clients; full error with
stack lands in logs keyed by request.id, response carries the id for support.

## Consequences
One `pkg/apperr` + tests over the mapping table; handlers contain zero ad-hoc
error strings.
