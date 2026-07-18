# ADR-0008: Kafka API (Redpanda) over RabbitMQ

Status: accepted · Date: 2026-07-18

## Context
Need ordered, replayable event streams for projections and scoring; also the
dominant broker expectation in fintech hiring.

## Decision
Kafka protocol. Local runtime: single-node Redpanda (one container, no ZooKeeper,
low RAM). Client: franz-go. Topics are created by an init job with explicit
partition counts (6) and keys per ADR-0009/architecture §5.

## Alternatives
RabbitMQ — great for task queues, but no log semantics: projections could not be
rebuilt by replay, per-key ordering is awkward. Apache Kafka proper — identical
API, heavier locally; Helm values allow swapping the image.

## Consequences
Consumer groups per service; lag exported to Prometheus; replayability is
demonstrated in the demo script (rebuild account projections from offset 0).
