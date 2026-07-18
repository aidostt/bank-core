# ADR-0016: Testing strategy

Status: accepted · Date: 2026-07-18

## Decision
Pyramid: (1) unit — domain logic, table-driven, mocks generated with moq/mockery
only at interfaces; target ≥80% on ledger/transfer domain+app; (2) integration —
testcontainers-go spins Postgres and Redpanda per package: repository tests, outbox
relay test, consumer dedup test, concurrent-transfer conservation test; (3) one e2e
test: compose up → register → open accounts → top up → P2P with FX → assert
balances and notification event; (4) load — k6 script for POST /transfers and
GET /accounts, results (RPS, p95/p99) committed to README; targets: p99 < 500ms
transfer, p95 < 100ms balance read at 200 RPS on laptop; (5) chaos — toxiproxy
between transfer and ledger: cut mid-saga, assert recovery worker completes or
compensates, money conserved (`make chaos`).
Contract safety: `buf breaking` in CI (Pact: roadmap).

## Consequences
CI runs 1+2; 3 runs in CI nightly-style job; 4-5 are documented make targets with
committed evidence.
