# ADR-0012: Business-level idempotency keys in transfer-service

Status: accepted · Date: 2026-07-18

## Context
Clients retry POSTs on timeouts; a retried transfer must not execute twice.
Gateway-level response caching (Redis) is a common but leaky shortcut: it ties
correctness to cache durability.

## Decision
`Idempotency-Key` header is mandatory on `POST /v1/transfers`. The key is passed
through to transfer-service, which stores `(customer_id, key) → transfer_id` with a
unique constraint **in the same transaction that creates the transfer**. A repeat
request returns the existing transfer (200 with current state). Keys expire after
24h (cleanup job). Redis at the gateway is used only for rate limiting.

## Alternatives
Redis SETNX + cached response at gateway — fast, but a Redis flush breaks the
guarantee and the gateway must replay bodies; correctness belongs to the domain
owner.

## Consequences
Same mechanism reused for top-ups. Conflict semantics (same key, different payload
→ 422) documented in the API spec.
