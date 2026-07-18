# ADR-0013: OpenTelemetry + Prometheus + Grafana + Jaeger, slog JSON

Status: accepted · Date: 2026-07-18

## Decision
OTel SDK in every service; traces exported OTLP→Jaeger all-in-one; metrics exposed
on `/metrics`, scraped by Prometheus; two Grafana dashboards provisioned from the
repo (Platform RED, Money Flow); logs are `log/slog` JSON to stdout with
`request.id` on every line. No OTel Collector locally (one hop less on a laptop);
the Helm values include an optional collector to show awareness. Loki: roadmap.

## Alternatives
ELK — heavy; Loki — nice-to-have, cut for RAM; vendor APM — not self-contained.

## Consequences
`pkg/otel` centralizes setup; propagation through gRPC metadata and Kafka headers is
tested (a trace of one transfer shows gateway→transfer→ledger→consumer spans —
screenshot in README).
