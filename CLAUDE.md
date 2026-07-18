# bank-core — Claude Code Master Context

Read this file fully before writing any code. It is the single source of truth for
conventions. Detailed designs live in `docs/`. Architecture decisions live in `docs/adr/`
— **never contradict an ADR silently**; if a decision must change, update the ADR first.

## What this project is

A portfolio-grade core-banking backend built as a set of independently deployable Go
microservices. It demonstrates strong-middle backend engineering for banking companies:
double-entry ledger, saga-orchestrated money transfers, transactional outbox, Kafka
event streaming with retries/DLQ, idempotency, observability, and production-style
testing. Target reviewer: a senior backend engineer at a large retail bank spending
10 minutes in this repo. Every decision must survive the question "why?" — the answer
is always in an ADR.

**Business scope:** retail online banking. Customers register, open KZT and USD
accounts, top up (mock), transfer between own accounts (with FX conversion) and to
other customers (P2P). Fraud scoring runs asynchronously and can freeze accounts.
Notifications are mock. All money amounts are integer minor units (tiyn/cents) —
**floats for money are forbidden everywhere**.

## Services

| Service | Type | Responsibility |
|---|---|---|
| `gateway` | HTTP (REST) | Edge API, JWT validation, rate limiting, routing to gRPC |
| `identity` | gRPC + JWKS HTTP | Users, credentials, JWT issuing, refresh sessions, RBAC |
| `account` | gRPC + Kafka consumer | Customer accounts registry, status (ACTIVE/FROZEN), balance projections |
| `ledger` | gRPC | **Source of truth for money.** Double-entry journal, holds, balances |
| `transfer` | gRPC + saga orchestrator | Transfer lifecycle, FX rates, limits, idempotency, outbox |
| `antifraud` | Kafka consumer | Async rule-based scoring, emits fraud alerts |
| `notification` | Kafka consumer | Mock email/push on transfer and fraud events |

Full designs: `docs/services/<name>.md`. System flows: `docs/architecture.md`.

## Tech stack (fixed — do not substitute)

- Go: latest stable, monorepo with `go.work`, one module per service + `pkg/` shared module.
- HTTP edge: Gin (gateway only). Internal RPC: gRPC + protobuf via `buf`.
- DB: PostgreSQL 16, one container, **separate database per service** (`identity_db`,
  `account_db`, `ledger_db`, `transfer_db`, `antifraud_db`, `notification_db`).
  Driver `pgx/v5`, queries via `sqlc`. Migrations via `golang-migrate`, embedded and
  run on service start.
- Messaging: Kafka API via **Redpanda** (single node). Client: `franz-go`.
- Cache/rate-limit: Redis (gateway only).
- Auth: JWT RS256, JWKS endpoint on identity, refresh tokens in `sessions` table.
- Observability: OpenTelemetry SDK → Jaeger (traces, OTLP), Prometheus (metrics,
  `/metrics` per service), Grafana (provisioned dashboards), `log/slog` JSON to stdout.
- Resilience: `sony/gobreaker` circuit breakers, timeouts + jittered retries on all
  gRPC clients, graceful shutdown everywhere.
- Testing: stdlib `testing` + `testify`, `testcontainers-go` (Postgres, Redpanda)
  for integration, one e2e transfer scenario, `k6` load scripts, `toxiproxy` chaos demo.
- CI: GitHub Actions — lint (`golangci-lint`), `buf lint` + `buf breaking`, unit +
  integration tests, build.
- Deploy: Docker Compose is the primary runtime (`make up`). Helm chart + k3d is the
  secondary demo (M3).

## Repository layout

```
bank-core/
├── CLAUDE.md
├── Makefile                    # up, down, demo, test, lint, generate, load, chaos
├── go.work
├── proto/                      # buf module, one package per service
├── gen/go/                     # buf generate output (committed)
├── pkg/                        # shared module: money, kafka, otel, grpcx, logging, config
├── services/
│   ├── gateway/ identity/ account/ ledger/ transfer/ antifraud/ notification/
│   │   ├── cmd/main.go         # manual DI wiring here, nowhere else
│   │   ├── internal/domain/    # entities, invariants, pure logic (no IO imports)
│   │   ├── internal/app/       # use cases / orchestration
│   │   ├── internal/adapters/  # postgres/, grpc/, kafka/, http/
│   │   ├── internal/config/
│   │   └── migrations/
├── deploy/
│   ├── compose/                # docker-compose.yml, compose.observability.yml, init-db.sql
│   ├── helm/bank-core/         # umbrella chart + per-service templates
│   └── k3d/
├── docs/                       # architecture.md, adr/, services/, demo.md
├── tests/e2e/
├── load/k6/
└── .github/workflows/ci.yml
```

## Architecture conventions

1. **Hexagonal-lite** in `ledger` and `transfer`: `domain` has zero infrastructure
   imports; `app` depends on interfaces; `adapters` implement them. Other services
   use a simpler layered structure but keep the same directory names.
2. **Manual DI** in `cmd/main.go` only. No DI frameworks.
3. Every gRPC server: interceptors chain = recovery → otel → logging → auth-claims.
   Every gRPC client: timeout (default 2s) → retry (3x, jitter, only idempotent
   methods) → circuit breaker.
4. Every Kafka consumer: dedup via `processed_messages(consumer, message_id)` table
   checked in the same DB transaction as the side effect; retry with exponential
   backoff; after max attempts publish to `<topic>.dlq` with error headers.
5. Every event is published through the **transactional outbox** of the owning
   service (outbox table + relay goroutine). Direct produce from request handlers
   is forbidden.
6. Correlation: gateway generates `X-Request-ID` → gRPC metadata → Kafka headers →
   every log line and span attribute (`request.id`).
7. Errors: domain errors are typed (`ErrInsufficientFunds`, `ErrAccountFrozen`, …),
   mapped once at the adapter edge to gRPC codes and once in gateway to HTTP
   problem+json. No `fmt.Errorf` string matching.
8. Config: env vars only, parsed into a struct at startup, fail fast on missing.
   `.env.example` committed, `.env` gitignored.
9. Money: `pkg/money` — `Amount{Value int64, Currency string}`; arithmetic helpers
   with overflow checks. Currencies: `KZT`, `USD` only.
10. API style: REST is `/v1/...`, kebab-case paths, problem+json errors, cursor
    pagination. Proto follows buf style guide.

## Definition of done (per milestone — see prompts/)

- `make up && make demo` runs the full happy path from a clean checkout.
- `make test` green; `make lint` clean.
- Each service has `docs/services/<name>.md` kept in sync with implementation.
- New decisions recorded as ADRs.

## Commands (implement in Makefile)

`make up` / `make up-observability` / `make down` / `make demo` (scripted end-to-end
scenario with curl, prints balances before/after) / `make test` / `make test-integration`
/ `make e2e` / `make lint` / `make generate` (buf + sqlc) / `make load` (k6) /
`make chaos` (toxiproxy demo) / `make helm-deploy` (k3d).

## Non-goals (do not build)

Frontend, real card processing, real KYC, OAuth2 authorization server, MFA, Vault
deployment, multi-region, exactly-once semantics. These are documented in
`docs/roadmap.md` with reasoning.
