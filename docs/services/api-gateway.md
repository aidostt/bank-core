# api-gateway

Thin edge. REST `/v1` → gRPC. No business logic, no database.

## Responsibilities
- Routing/translation REST↔gRPC (hand-written handlers, Gin; OpenAPI spec is the
  contract, served at `/v1/openapi.yaml`, generated types via oapi-codegen).
- AuthN: JWT RS256 validation against identity JWKS (cached; refetch on unknown kid).
- AuthZ: route-level role table (customer/support/admin).
- Injects `x-customer-id`, `x-roles`, `x-request-id` into gRPC metadata.
- Rate limiting: Redis fixed window, per user+route (defaults: 10 rps reads,
  2 rps transfers), 429 + Retry-After. Redis down → fail open + warn (ADR in §7).
- Requires `Idempotency-Key` on POST /v1/transfers and /v1/topups (400 if missing),
  passes it through.
- Maps gRPC statuses → RFC 9457 problem+json (single mapping table, tested).
- Timeouts per route (reads 1s, transfers 5s), body limits, security headers, CORS.

## REST surface (v1)
```
POST /v1/auth/register | /v1/auth/login | /v1/auth/refresh | /v1/auth/logout
GET  /v1/customers/me
POST /v1/accounts                      # open account (currency)
GET  /v1/accounts                      # own accounts + projected balances (as_of)
GET  /v1/accounts/{id}/transactions    # cursor pagination
POST /v1/topups                        # mock funding
POST /v1/transfers                     # Idempotency-Key required
GET  /v1/transfers/{id}                # poll saga state
GET  /v1/transfers                     # history
GET  /v1/rates
-- support/admin --
GET  /v1/admin/customers/{id}/accounts
POST /v1/admin/accounts/{id}/freeze | /unfreeze
```

## Testing / DoD
Handler tests with fake gRPC backends · authz table test · rate-limit test ·
problem+json mapping test. DoD: full demo scenario passes via these routes; OpenAPI
lints clean; JWT expiry & tampering covered by tests.
