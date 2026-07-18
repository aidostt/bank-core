# ADR-0003: REST edge, gRPC internal, Kafka async

Status: accepted · Date: 2026-07-18

## Context
Three kinds of communication: public client API, service-to-service calls in the
request path, and facts that other services react to.

## Decision
- Public: REST/JSON `/v1` at the gateway (universal client compatibility, easy demo).
- Internal sync: gRPC/protobuf (typed contracts, deadlines, interceptors, streaming
  headroom; standard in banking backends).
- Async: Kafka events for anything not needing an immediate answer (projections,
  fraud, notifications).

Rule: a sync call is allowed only when the caller cannot proceed without the answer
(transfer→ledger, transfer→account validation). Everything else is an event.

## Alternatives
REST everywhere — rejected: weaker contracts, hand-written clients.
gRPC to public clients — rejected: browser/demo friction, no gain for portfolio.

## Consequences
buf module is the internal contract source; `buf breaking` guards compatibility.
Webhooks for external partners: roadmap.
