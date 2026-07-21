#!/usr/bin/env bash
# make helm-deploy (prompts/M3.md §3): create a k3d cluster, import the
# locally built images, install the umbrella chart and smoke-test the
# gateway. Requires: k3d, kubectl, helm, docker.
set -euo pipefail

CLUSTER=bank-core
CHART="$(dirname "$0")/../deploy/helm/bank-core"
SERVICES=(gateway identity account ledger transfer antifraud notification)

step() { printf '\n\033[1;36m☸ %s\033[0m\n' "$*"; }

step "building service images (compose builder, shared cache)"
docker compose -f "$(dirname "$0")/../deploy/compose/docker-compose.yml" --profile core build

step "creating k3d cluster '$CLUSTER' (idempotent, self-healing)"
# Reuse only a *reachable* cluster; a half-created one left behind by a
# crashed Docker daemon must be recreated, not imported into.
if k3d cluster list | grep -q "^$CLUSTER" && kubectl --context "k3d-$CLUSTER" cluster-info >/dev/null 2>&1; then
  echo "cluster exists and is reachable — reusing"
else
  echo "creating a fresh cluster"
  k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
  k3d cluster create "$CLUSTER" --agents 1 -p "30080:30080@server:0" --wait
fi

step "importing images into the cluster"
for s in "${SERVICES[@]}"; do
  k3d image import "bank-core-$s:latest" -c "$CLUSTER"
done

step "helm upgrade --install"
helm upgrade --install bank-core "$CHART" --wait --timeout 5m

step "smoke: gateway /healthz through the mapped NodePort"
for i in $(seq 1 30); do
  if curl -fsS http://localhost:30080/healthz >/dev/null 2>&1; then
    echo "✔ gateway healthy on k3d"
    step "smoke: register+login through the k8s stack"
    RUN=$(date +%s)
    curl -fsS -X POST http://localhost:30080/v1/auth/register -H 'Content-Type: application/json' \
      -d "{\"email\":\"helm-$RUN@demo.kz\",\"password\":\"helm-pass-123\",\"name\":\"Helm Smoke\"}" >/dev/null
    TOKEN=$(curl -fsS -X POST http://localhost:30080/v1/auth/login -H 'Content-Type: application/json' \
      -d "{\"email\":\"helm-$RUN@demo.kz\",\"password\":\"helm-pass-123\"}" | jq -r .access_token)
    curl -fsS http://localhost:30080/v1/customers/me -H "Authorization: Bearer $TOKEN" | jq '{id, email}'
    echo "✔ helm smoke passed"
    exit 0
  fi
  sleep 5
done
echo "✘ gateway never became healthy on k3d" >&2
kubectl get pods -A >&2
exit 1
