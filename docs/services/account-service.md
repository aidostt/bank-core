# account-service

Account registry + balance projections. Registry writes are strongly consistent;
balances are an eventually-consistent read model (ADR-0006).

**M2 status:** the projection consumer (version-guarded), the fraud-freeze
consumer, the `customers.registered` bootstrap consumer and the
`accounts.events` outbox+relay are live; the read API serves projected
balances with `as_of`. The M1 lazy customer upsert remains as a harmless
idempotent fallback.

**M2 note on holds:** holds are not projected (TransactionPosted carries
`{balance_after, version}` only), so the read API's `available` equals the
settled balance; in-flight holds are visible only inside the ledger. Honest
staleness applies: the `as_of` timestamp is the journal entry time.

## Responsibilities
- Open account: generate account number (KZ-prefixed, check digit), currency
  KZT|USD, ACTIVE; call ledger `CreateAccount` synchronously (idempotent) so a
  ledger mirror exists before any money can move; emit `accounts.events`.
- Status management: ACTIVE ⇄ FROZEN ⇄ CLOSED (admin/support or fraud-driven).
- **Projection consumer** of `ledger.transactions`: upsert
  `balances(account_id, balance, version, as_of)` guarded by
  `WHERE excluded.version > balances.version` — reordering-safe; dedup via
  `processed_messages` in the same tx.
- **Freeze consumer** of `fraud.alerts`: severity=HIGH → set FROZEN, emit
  `accounts.events:AccountFrozen{reason}`.
- Read API: own accounts with balances (`as_of` timestamp exposed honestly),
  transactions listing proxied to ledger `ListPostings` (owner-scoped).

## gRPC API
`OpenAccount, GetAccount, ListAccountsByCustomer, ResolveByNumber (for P2P
destination), Freeze, Unfreeze, GetBalances`.

## Schema
`customers(id, user_id uniq, tier)` · `accounts(id, customer_id, number uniq,
currency, status, opened_at)` · `balances` · `processed_messages` · `outbox`.

## Testing / DoD
Integration: projection applies out-of-order events correctly (send v3 before v2);
duplicate event → no double apply; freeze flow end-to-end (alert → frozen →
transfer rejected). DoD: replay demo — reset consumer group to offset 0, projections
converge to `make verify-ledger` truth.
