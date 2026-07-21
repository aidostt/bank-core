# ADR-0015: Docker Compose primary, Helm + k3d secondary

Status: accepted · Date: 2026-07-18

## Decision
`make up` (compose) is the supported way to run everything; profiles split core
(~11 containers) from observability (3 more) to fit 16 GB RAM. Kubernetes story:
one umbrella Helm chart (per-service subcharts sharing a library template:
Deployment, Service, ConfigMap, probes, resources) deployed to k3d by
`make helm-deploy`. Terraform/cloud: roadmap (no budget dependency for reviewers).

## Alternatives
K8s-only — hostile to a reviewer who just wants `make up`. Kustomize — fine, Helm
chosen for hiring-signal and templating of 7 near-identical services.

## Consequences
Compose and Helm share images built by the same multi-stage Dockerfiles (distroless
final stage, non-root).

## Implementation note (M3)
Two realized deviations, both documented in [docs/deploy.md](../deploy.md):
- The "per-service subcharts sharing a library template" is realized as a single
  `range` template over `values.yaml`, because the seven services are deliberately
  identical in shape — a real library subchart would be ceremony without payoff at
  this scale. The Deployment/Service/probes/resources/HPA contract is unchanged.
- Infra (postgres/redpanda/redis) ships as plain in-chart templates with ephemeral
  storage rather than pinned dependency subcharts: zero external chart deps keeps
  the k3d smoke reproducible offline. Persistent volumes and per-service Postgres
  stay on the roadmap.
