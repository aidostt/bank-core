# ADR-0006: Double-entry ledger as the single source of money truth

Status: accepted · Date: 2026-07-18

## Context
The most common design failure in banking demos is a mutable `balance` column
updated by whoever feels like it. Real banks derive balances from an immutable
double-entry journal.

## Decision
`ledger` owns all money. Every money movement is a journal entry with ≥2 postings;
per entry, postings **sum to zero per currency** (enforced in code and by a DB
constraint via trigger-checked aggregate on commit). Entries and postings are
immutable — corrections are new reversal entries. `account_balances` is a
materialized running balance updated in the same DB transaction as the postings,
with a monotonic `version` per account. Customer-visible balances in
account-service are projections from events, never authoritative.

Internal (non-customer) ledger accounts exist for cash-in (top-up funding),
fx_position_kzt, fx_position_usd, and fees (future).

## Alternatives
Balance column in account-service with compensating updates — rejected: no audit
trail, drift is undetectable, fails any banking interview.
Event-sourced ledger with balance recomputed on read — rejected for scope: the
materialized-balance journal gives the same guarantees with simpler ops.

## Consequences
`sum(postings) == 0` and `balance == sum(postings for account)` become testable
invariants (property-style tests). Top-ups are journal entries against the cash-in
account, so even mock funding is honest double-entry.

M1 interim (per prompts/M1.md — projections are M2): account-service serves
read-API balances via a synchronous ledger `GetBalances` call. The M2
projection consumer replaces that path; the "projections, never authoritative"
rule applies from M2 on. Flagged in docs/services/account-service.md.
