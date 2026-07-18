# ADR-0004: PostgreSQL, database-per-service in one cluster

Status: accepted · Date: 2026-07-18

## Context
Each service must own its data with no shared tables, on a 16 GB laptop.

## Decision
One PostgreSQL 16 container; a separate logical database per service, separate
credentials, created by `init-db.sql`. No cross-database queries ever.

## Alternatives
One cluster per service — production-realistic but 6 containers of pure overhead
for a demo; the isolation property demonstrated is identical at the schema level.
Shared database/schema — rejected: destroys service autonomy, the exact anti-pattern
interviewers probe for.

## Consequences
The ops difference (per-service clusters, backups, failover) is acknowledged in
`docs/roadmap.md`. Read replicas and sharding: not implemented; the ledger doc
contains the scaling plan (replicas for read APIs, partition-by-account-hash as the
sharding path) so the reasoning is visible.
