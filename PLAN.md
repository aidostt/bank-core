# bank-core — Build Plan (2-day sprint)

Quality gate for every milestone: `make lint` clean, `make test` green, docs updated,
no TODOs on the money path. If time runs out, a finished M1+M2 beats a rushed M3 —
cut from the bottom of M3, never from tests.

## M1 — Money moves correctly (~day 1)
Scaffold monorepo (go.work, pkg/, buf, sqlc, golangci-lint, Makefile, compose core
profile) → identity (register/login/refresh/JWKS) → account (open/resolve, no
projections yet) → **ledger complete** (all invariants + concurrent test) →
**transfer complete** (saga, idempotency, recovery worker) → gateway (auth, routes,
problem+json, Idempotency-Key, rate limit) → outbox relays in ledger+transfer →
`make demo`: register two users → open KZT+USD accounts → top up → INTERNAL FX
transfer → P2P → print balances → `make verify-ledger`.
**Acceptance:** demo passes from clean checkout; concurrent conservation test green;
ambiguous-timeout recovery integration test green.

## M2 — Distributed behaviors + eyes (~day 1 evening → day 2 morning)
account projections consumer (version-guarded) + freeze consumer → antifraud rules
+ alerts → notification consumer → DLQ wiring + `make dlq-inspect` → OTel everywhere
+ compose obs profile + 2 Grafana dashboards → e2e test in tests/e2e → replay demo
(reset offsets → converge).
**Acceptance:** e2e green in CI; one Jaeger trace spans gateway→transfer→ledger→
consumer; velocity fraud scenario freezes account and blocks next transfer.

## M3 — Proof + delivery (~day 2)
k6 scripts + run + paste results (RPS, p95/p99) into README → toxiproxy chaos
(`make chaos`) with output in README → Helm umbrella chart + k3d (`make helm-deploy`)
→ CI workflow (lint, buf lint+breaking, unit+integration, build; e2e job) → root
README (banner, architecture diagram, quickstart, screenshots: Grafana, Jaeger,
k6) → final docs/ADR sync pass.
**Acceptance:** a stranger reproduces demo+load+chaos with three commands; CI green
badge; README answers "why" for every major choice via ADR links.
