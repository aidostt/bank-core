# ADR-0010: Orchestrated saga with hold/capture for transfers

Status: accepted · Date: 2026-07-18

## Context
A transfer spans validation (account), funds guarantee and posting (ledger), and
follow-up events. Ambiguous failures (timeout during posting) must never lose or
double-move money.

## Decision
transfer-service is an explicit orchestrator with a persisted state machine
(states in architecture §4; every change appended to `transfer_events`).
Two-phase money movement: `PlaceHold` (reserves available balance) then
`PostTransaction` (captures against the hold). Compensation is `ReleaseHold`.
All ledger operations are idempotent by `transfer_id`, so the recovery worker can
safely re-drive stuck transfers after crashes/timeouts by re-sending or querying
`GetTransactionByReference`.

Fraud is **not** in the critical path: only a cheap local limits check runs inline;
scoring is async and can freeze the account for future operations (product
decision: availability of transfers over inline scoring latency/coupling; documented
trade-off).

## Alternatives
Choreography — fewer components but transfer state is smeared across topics; hard
to answer "where is transfer X and why". Orchestration is the standard in payment
systems and demos the pattern interviewers ask about.
Single-shot post without hold — simpler but leaves the saga with no real
compensation step, i.e. nothing to demonstrate; hold/capture also mirrors real
authorization flows.

## Consequences
Recovery worker + toxiproxy chaos demo prove crash-safety. The state machine table
doubles as an audit trail.
