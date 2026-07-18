# ADR-0002: Monorepo with Go workspaces

Status: accepted · Date: 2026-07-18

## Context
Seven Go services plus shared code, one developer, atomic cross-service changes
(proto updates) are frequent.

## Decision
Single repository. One Go module per service + one `pkg/` shared module, tied with
`go.work`. Generated proto code committed under `gen/go` as its own module.

## Alternatives
Polyrepo — rejected: cross-cutting changes need 7 PRs; painful solo.
Single module — rejected: blurs service boundaries, one service's deps leak to all.

## Consequences
CI paths-filters can build only changed services. Shared `pkg/` must stay small and
dependency-light (no service imports another service's internal code — enforced by
`internal/`).
