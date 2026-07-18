# ADR-0009: At-least-once + transactional outbox + consumer dedup

Status: accepted · Date: 2026-07-18

## Context
Dual-write (commit DB then produce) loses or duplicates events on crashes.
Exactly-once end-to-end across DB+broker+side-effects does not exist in practice.

## Decision
Producers: every event is inserted into the service's `outbox` table inside the
business transaction; a relay goroutine publishes pending rows (batch, ordered per
aggregate) and marks them sent. Redelivery is possible → at-least-once.
Consumers: idempotent handlers; `processed_messages(consumer_group, event_id)`
inserted in the same DB transaction as the side effect; duplicate event_id → ack
and skip. Event IDs are UUIDv7 from the producer.

## Alternatives
Debezium CDC — the industrial version of the same idea; rejected as an extra moving
part for a demo; noted in roadmap. Kafka transactions/EOS — covers broker-side only,
not DB side effects; adds complexity without removing the need for dedup.

## Consequences
Outbox lag metric + alert threshold documented. Relay is per-service but the code
lives once in `pkg/outbox`.
