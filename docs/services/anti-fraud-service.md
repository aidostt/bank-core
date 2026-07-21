# anti-fraud-service

Async rule-based scoring. Consumes facts, emits alerts. No inline path (ADR-0010).

## Responsibilities
- Consume `transfers.status` (COMPLETED) and `ledger.transactions`.
  **M2 note:** only `transfers.status` is consumed — all four shipped rules
  are transfer-based; `ledger.transactions` subscription joins when a rule
  needs raw postings (e.g. cash-structuring detection).
- Maintain per-customer sliding stats in Postgres (`customer_stats`: counts/sums
  per window, updated in consumer tx).
- Rules (config-driven, table `rules(id, kind, params jsonb, severity, enabled)`):
  R1 amount > threshold (per currency) → MEDIUM; R2 velocity: >N transfers in 5m →
  HIGH; R3 daily out-sum > limit → HIGH; R4 new beneficiary + large amount → MEDIUM.
- Emit `fraud.alerts:FraudAlertRaised{customer_id, transfer_id, rule, severity,
  details}` via outbox. HIGH alerts freeze accounts via account-service consumer.
- Standard consumer kit: dedup, backoff retries, DLQ.

## Schema
`customer_stats` · `alerts(id, customer_id, transfer_id, rule_id, severity,
details jsonb, created_at)` · `beneficiaries(customer_id, counterparty_account,
first_seen)` · `rules` · `processed_messages` · `outbox`.

## Testing / DoD
Rule unit tests (table-driven, boundary values) · integration: velocity scenario
produces HIGH alert and downstream freeze (asserted in e2e) · DLQ path test
(poison message lands in DLQ with headers after 5 attempts).
