# account-service

Account registry + balance projections. Registry writes are strongly consistent;
balances are an eventually-consistent read model (ADR-0006).

**M1 notes (per prompts/M1.md — projections/consumers are M2):**
- The customer row is bootstrapped lazily on first `OpenAccount` (upsert by
  user_id); M2 replaces this with the `customers.registered` consumer.
- Balances in the read API are served by a synchronous ledger `GetBalances`
  call until the M2 projection consumer lands (interim noted in ADR-0006).
- `accounts.events` outbox emission ships in M2 together with the relay
  (PLAN M1 wires relays in ledger+transfer only); the tables exist.

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
