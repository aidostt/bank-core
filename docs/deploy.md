# Deployment

## Compose (primary — ADR-0015)

- `make up` — core profile: 7 services, postgres (db-per-service via
  init-db.sql), redpanda (+ explicit topic init), redis, toxiproxy.
- `make up-observability` — adds the obs profile: jaeger (OTLP), prometheus,
  grafana (Platform RED + Money Flow provisioned from the repo), image
  renderer for committed screenshots.
- `make down` — tears everything down including volumes.

Both profiles share one network; core services always point OTLP at
`jaeger:4317` and export quietly when the obs profile is not running.

## Helm + k3d (secondary demo — prompts/M3.md §3)

`make helm-deploy` = build images → `k3d cluster create bank-core` →
`k3d image import` → `helm upgrade --install` → smoke curl through the
gateway NodePort (30080): healthz, register, login, `GET /v1/customers/me`.

Chart shape (`deploy/helm/bank-core`):
- One shared template renders every service Deployment/Service: probes on
  `/healthz`/`/readyz`, resources, env from `values.yaml` — the "library
  subchart" idea realized as a single `range` template because the seven
  services are deliberately identical in shape.
- HPA stub included, disabled by default (`hpa.enabled`).
- **Deviation from the M3 prompt** ("infra as subcharts or pinned
  dependencies"): postgres/redpanda/redis are plain in-chart templates with
  ephemeral storage. Reasoning: zero external chart dependencies keeps the
  k3d smoke reproducible offline and reviewable in one file; production
  infra would come from operators/managed services anyway, which no local
  subchart honestly represents.

Not covered on purpose (roadmap): persistent volumes, per-service Postgres,
ingress/TLS, OTel collector, Argo Rollouts.
