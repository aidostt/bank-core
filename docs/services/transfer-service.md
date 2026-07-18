# transfer-service

**Saga orchestrator for money movement.** Owns transfer lifecycle, limits, FX
rates, idempotency keys, outbox, recovery. Hexagonal core; the state machine is
pure domain code. Related ADRs: 0009, 0010, 0012.

## Responsibilities
- `CreateTransfer` (P2P, own-to-own, top-up) with mandatory idempotency key.
- Validate: account existence/ownership/status via account-service gRPC, currency
  rules, per-transaction and daily limits (limits config seeded per customer tier).
- FX: pick rate from local `rates` table (seeded KZT/USD buy+sell), lock it on the
  transfer row, compute counter-amount (banker's rounding documented in
  `pkg/money`).
- Drive the saga against ledger (hold → post → complete / release → fail).
- Persist every state change to `transfer_events` (append-only audit).
- Emit `transfers.status` events via outbox.
- Recovery worker for stuck sagas; expiry of stale idempotency keys.

Non-responsibilities: balances (ledger), account status changes (account-service),
fraud scoring (antifraud; only cheap inline limit checks here).

## State machine (persisted, exhaustive)

```
CREATED ─▶ VALIDATING ─▶ HELD ─▶ POSTING ─▶ COMPLETED
              │            │        │
              ▼            ▼        ▼ (unknown outcome)
            FAILED ◀─ RELEASING ◀── recovery: query ledger by transfer_id
                                    ├─ entry exists → COMPLETED
                                    └─ absent → retry PostTransaction (idempotent)
                                               or RELEASING → FAILED after budget
```

Transition rules are a pure function `next(state, event) (state, actions, error)` —
illegal transitions return `ErrIllegalTransition` and are unit-tested exhaustively.
`transfers.state` updates and `transfer_events` insert happen in one DB tx together
with any outbox record.

## Transfer types
| type | legs | notes |
|---|---|---|
| `TOPUP` | cash_in → customer acct | mock funding; same saga, no ownership check on source |
| `INTERNAL` | own acct → own acct | may cross currency → 4-posting FX entry |
| `P2P` | customer → customer | destination resolved by account number; may cross currency |

## gRPC API
```
service Transfer {
  rpc CreateTransfer(CreateTransferRequest) returns (TransferView);  // metadata: customer claims + idempotency key
  rpc GetTransfer(GetTransferRequest) returns (TransferView);
  rpc ListTransfers(ListTransfersRequest) returns (TransfersPage);   // cursor, owner-scoped
  rpc GetRates(GetRatesRequest) returns (Rates);
}
```
`CreateTransfer` returns immediately after the saga completes synchronously in the
happy path (target p99 < 500ms); if a downstream timeout leaves state ambiguous the
call returns the transfer in its current state (`POSTING`) with `202`-semantics at
the gateway — clients poll `GetTransfer`. This behavior is explicitly documented in
the API spec (honest distributed-systems UX, not hidden).

## Idempotency
Table `idempotency_keys(customer_id, key, transfer_id, request_hash, created_at)`
unique on (customer_id, key), written in the transfer-creation tx. Same key + same
`request_hash` → return existing transfer; same key + different hash → 422
`IDEMPOTENCY_CONFLICT`. TTL 24h via cleanup job.

## Ledger client policy
Deadline 2s per call; retries only on UNAVAILABLE/DEADLINE_EXCEEDED for the
idempotent ledger methods (they all are, by design), max 3, exponential + jitter;
circuit breaker (gobreaker: open after 5 consecutive failures, half-open probe 10s).
Breaker open → fail fast `FAILED_PRECONDITION_UPSTREAM` → gateway 503.

## Recovery worker
Every 5s: `SELECT ... FROM transfers WHERE state IN ('HELD','POSTING') AND
updated_at < now() - interval '15 seconds' FOR UPDATE SKIP LOCKED LIMIT 10` —
re-drive each through the state machine (safe on multiple instances thanks to
SKIP LOCKED). Retry budget per transfer (5 attempts) then RELEASING → FAILED with
`reason=recovery_exhausted`. Metric `transfer_recoveries_total{outcome}`.

## Events produced
`transfers.status`, key = transfer_id: `TransferCompleted{transfer, applied_rate?}`,
`TransferFailed{transfer, reason}`. Consumed by notification (both) and antifraud
(completed only).

## Schema
`transfers(id uuidv7, type, state, customer_id, from_account, to_account,
amount, currency, counter_amount?, counter_currency?, applied_rate?, reason?,
created_at, updated_at)` · `transfer_events(id, transfer_id, from_state, to_state,
detail jsonb, at)` · `idempotency_keys` · `rates` · `limits` · `outbox` ·
index on `(state, updated_at)` for the recovery scan.

## Testing focus
Exhaustive state-machine unit tests · integration: happy path, insufficient funds,
frozen destination, FX rounding cases · **chaos: toxiproxy cuts ledger connection
after PlaceHold and separately during PostTransaction; assert recovery resolves and
`make verify-ledger` shows conservation** · idempotency replay under concurrency
(same key, 20 parallel requests → exactly one transfer).

## Definition of done
Chaos demo scripted (`make chaos`) with output pasted into README · ambiguous-
timeout path visible in a Jaeger trace · limits and FX documented in API spec.
