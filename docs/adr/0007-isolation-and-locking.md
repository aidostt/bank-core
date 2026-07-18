# ADR-0007: Read Committed + ordered SELECT FOR UPDATE

Status: accepted · Date: 2026-07-18

## Context
Concurrent postings on the same accounts must not produce lost updates, negative
available balances, or deadlocks.

## Decision
Ledger transactions run at Read Committed. Before writing postings, the involved
`account_balances` rows are locked with `SELECT ... FOR UPDATE` **in a canonical
order (ascending account UUID)** to make deadlocks impossible by construction.
Available-balance check (`balance - active_holds >= debit`) happens after the lock.

## Alternatives
Serializable + retry-on-40001 — correct and lock-free in the optimistic sense, but
under hot-account contention it degrades to a retry storm; the retry loop also
complicates every caller. Chosen approach gives predictable latency and is the
dominant pattern in payment ledgers.
Advisory locks — equivalent power, less visible to a reviewer than row locks.

## Consequences
Lock ordering is centralized in one repository method and covered by a concurrent
integration test (100 goroutines, two accounts, assert conservation of money and
zero deadlocks). Documented trade-off table lives in the ledger service doc.
