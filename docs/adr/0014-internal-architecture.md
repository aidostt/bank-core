# ADR-0014: Hexagonal-lite for ledger/transfer, layered elsewhere; manual DI

Status: accepted · Date: 2026-07-18

## Context
Complex domain logic (invariants, state machines) benefits from isolation; CRUD-ish
services don't earn the ceremony. Uniform layout still wanted.

## Decision
All services share the layout `domain / app / adapters / config`. In ledger and
transfer, `domain` is strictly pure (no IO imports, enforced by depguard) and `app`
depends only on interfaces defined app-side. Other services may keep thinner
domain packages. Dependency wiring is manual in `cmd/main.go`. CQRS: not adopted;
the account balance projection is a read model by nature, which is called out in
docs — a dedicated search/reporting read side is roadmap.

## Alternatives
Uniform full hexagonal — boilerplate in trivial services obscures the signal.
wire/fx — indirection without payoff at this scale.

## Consequences
Ledger domain is testable without containers (pure invariant tests + property test
for conservation of money).
