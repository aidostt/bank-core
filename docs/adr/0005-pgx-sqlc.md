# ADR-0005: pgx/v5 + sqlc

Status: accepted · Date: 2026-07-18

## Context
Need type-safe, reviewable SQL with real Postgres features (partitioning,
`FOR UPDATE`, CTEs), plus explicit transaction control for outbox/dedup patterns.

## Decision
`pgx/v5` pool as the driver; `sqlc` generates typed query code from committed `.sql`
files. Transactions are managed in the app layer via a small `TxManager` in `pkg/`.

## Alternatives
GORM — rejected: hides SQL, awkward for locking/partitioning, poor interview signal
for banking. sqlx — viable, but sqlc adds compile-time checking of every query and
removes scan boilerplate. squirrel — dynamic SQL not needed here.

## Consequences
`make generate` runs sqlc; queries live next to migrations; reviewers see every SQL
statement verbatim.
