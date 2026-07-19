# ledger-service

**The source of truth for money.** Double-entry journal with holds, materialized
balances, monthly partitioning, transactional outbox. Pure hexagonal core.
Related ADRs: 0006, 0007, 0009, 0017.

## Responsibilities
- Maintain ledger accounts (customer-mirrored + internal: `cash_in`,
  `fx_position_kzt`, `fx_position_usd`).
- Accept journal entries (≥2 postings, zero-sum per currency), immutable once posted.
- Manage holds (reserve available balance) and capture/release them.
- Materialize running balances atomically with postings; version them.
- Emit `ledger.transactions` events via outbox.
- Answer balance/statement queries (internal gRPC; customer reads go through
  account-service projections).

Explicit non-responsibilities: knowing *why* money moves (transfer semantics, FX
rates, limits — transfer-service), account lifecycle/KYC (account-service).

## Domain model & invariants

Entities: `LedgerAccount{id, external_account_id?, type: customer|internal,
currency, status}`, `JournalEntry{id, reference_type, reference_id, occurred_at,
postings[]}`, `Posting{entry_id, account_id, amount int64 (signed, minor units),
currency}`, `Hold{id, account_id, amount, reference_id, status: active|captured|
released, expires_at}`, `AccountBalance{account_id, balance, held, version}`.

Invariants (each has a dedicated test):
1. Per journal entry, per currency: `Σ postings = 0`.
2. Entries/postings are never updated or deleted; corrections = reversal entries.
3. For customer accounts: `balance − held ≥ 0` at all times (internal accounts may
   go negative — they absorb float, e.g. fx positions).
4. `AccountBalance.balance ≡ Σ postings(account)` — checked by a property test and
   a `make verify-ledger` reconciliation query.
5. `version` increases by exactly 1 per balance change (drives safe projections).
6. One journal entry per `(reference_type, reference_id)` — idempotency (unique index).
7. Capturing a hold posts at most the held amount and transitions it exactly once.

## gRPC API (proto sketch)

```
service Ledger {
  rpc CreateAccount(CreateAccountRequest) returns (LedgerAccount);          // idempotent by external_account_id
  rpc PlaceHold(PlaceHoldRequest) returns (Hold);                           // idempotent by reference_id
  rpc ReleaseHold(ReleaseHoldRequest) returns (Hold);                       // idempotent
  rpc PostTransaction(PostTransactionRequest) returns (JournalEntry);       // idempotent by reference_id; optional hold_id to capture
  rpc GetTransactionByReference(GetTransactionByReferenceRequest) returns (JournalEntry); // NOT_FOUND if absent — recovery worker uses this
  rpc GetBalances(GetBalancesRequest) returns (Balances);
  rpc ListPostings(ListPostingsRequest) returns (PostingsPage);             // time-range required (partition pruning), cursor pagination
}
```

Error mapping: `INSUFFICIENT_FUNDS`, `ACCOUNT_FROZEN`, `CURRENCY_MISMATCH`,
`UNBALANCED_ENTRY` → `FAILED_PRECONDITION` with typed errdetails;
duplicate reference with different payload → `ALREADY_EXISTS`.

## Posting algorithm (the heart)

```
BEGIN;                                             -- Read Committed
  SELECT ... FROM account_balances
   WHERE account_id = ANY($ids ORDER BY id ASC)    -- canonical lock order
   FOR UPDATE;
  -- idempotency: SELECT entry by (reference_type, reference_id); if found → COMMIT, return it
  -- validate: currencies, zero-sum per currency, frozen status
  -- if hold_id: validate hold active & sufficient; mark captured
  -- check available (balance - held + posting) >= 0 for customer accounts
  INSERT journal_entries ...; INSERT postings ...;
  UPDATE account_balances SET balance = balance + Δ, version = version + 1 ...;
  INSERT outbox (event: TransactionPosted{entry, per-account {balance, version}});
COMMIT;
```

Concurrency test: 100 goroutines × random transfers between 4 accounts →
assert total money conserved, no deadlocks, no negative available balances.

## Schema (migrations outline)

`ledger_accounts`, `journal_entries` (PARTITION BY RANGE (occurred_at)),
`postings` (PARTITION BY RANGE (occurred_at), FK to entries),
`holds` (+ index on (account_id) WHERE status='active'),
`account_balances`, `outbox(id uuidv7, topic, key, payload bytea, created_at,
sent_at NULL)` with index on `sent_at IS NULL`.
Startup task ensures partitions for current and next month exist.

Implementation notes:
- The invariant-6 unique index cannot live on the partitioned tables (a
  partitioned unique index must include the partition key), so idempotency is
  enforced by the plain `journal_entry_refs(reference_type, reference_id →
  entry_id, occurred_at)` table written in the same transaction.
- `cash_in` is per-currency (`cash_in_kzt`, `cash_in_usd`): every ledger
  account carries exactly one currency, and a top-up leg must match the
  funded account's currency.
- The zero-sum constraint trigger is attached per postings partition (plain
  tables); the partition task creates it for every new monthly partition.
- Lock order (ADR-0007) is: hold row first (if capturing), then balances in
  ascending account id — uniform across PostTransaction/ReleaseHold/sweeper,
  which makes lock cycles impossible by construction.

## Events produced

Topic `ledger.transactions`, key = each affected `account_id` (one event per
affected account, carrying that account's `{balance_after, version}` plus the full
entry) — this gives per-account ordering for projections. Envelope per
architecture §5.

## Failure & ops
- Relay: batch 100, 200ms tick, per-key order preserved, `outbox_lag` gauge.
- Readiness = DB ping; no broker dependency for serving gRPC (outbox decouples).
- Expired holds: sweeper releases holds past `expires_at` (default 10m) and logs a
  warning — protects against orchestrator death mid-saga.

## Testing focus
Pure domain tests (zero-sum, hold lifecycle), repository integration tests
(idempotent replay, lock order, partition insert), property test for conservation,
reconciliation query, outbox relay test with broker kill/restart.

## Definition of done
All invariants tested · concurrent test green · `make verify-ledger` clean after
demo + load run · trace shows PostTransaction span with DB child spans.
