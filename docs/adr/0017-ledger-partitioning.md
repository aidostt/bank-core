# ADR-0017: Range partitioning of postings by month

Status: accepted · Date: 2026-07-18

## Context
`postings` is the unbounded high-write table; banks retain years of it.

## Decision
`postings` (and `journal_entries`) are declaratively range-partitioned by
`occurred_at` month. Partitions for current+next month are created by a startup
task. Indexes are defined on the partitioned parent. Queries always carry a time
range or go via account+recent index.

## Alternatives
pg_partman — right tool in production, an extra dependency here; hash partitioning —
wrong axis (queries are time-scoped); nothing — works for a demo but forfeits an
easy, honest production signal.

## Consequences
Retention/archival becomes `DETACH PARTITION` (documented, not automated).
