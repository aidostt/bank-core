#!/usr/bin/env bash
# make helm-deploy (prompts/M3.md §3): create a k3d cluster, import the
# locally built images, install the umbrella chart and smoke-test the
# gateway. Requires: k3d, kubectl, helm, docker.
set -euo pipefail

CLUSTER=bank-core
CHART="$(dirname "$0")/../deploy/helm/bank-core"
SERVICES=(gateway identity account ledger transfer antifraud notification)
# Pin a k3s image that still supports cgroup v1. k3d's default is the latest
# k3s, which dropped cgroup v1 — on a Docker host running cgroup v1 (e.g.
# Docker Desktop / WSL2 without cgroupns), that kubelet refuses to start and
# the cluster never becomes Ready. This tag runs on both cgroup v1 and v2.
K3S_IMAGE="${K3S_IMAGE:-rancher/k3s:v1.31.7-k3s1}"

step() { printf '\n\033[1;36m☸ %s\033[0m\n' "$*"; }

step "building service images (compose builder, shared cache)"
docker compose -f "$(dirname "$0")/../deploy/compose/docker-compose.yml" --profile core build

step "creating k3d cluster '$CLUSTER' (idempotent, self-healing)"
# Reuse only a *reachable* cluster; a half-created one left behind by a
# crashed Docker daemon must be recreated, not imported into.
if k3d cluster list | grep -q "^$CLUSTER" && kubectl --context "k3d-$CLUSTER" get nodes >/dev/null 2>&1; then
  echo "cluster exists and is reachable — reusing"
else
  echo "creating a fresh cluster"
  k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
  # NOTE: no `--wait`. On Docker Desktop `k3d cluster create --wait` can hang
  # indefinitely on the post-start readiness probe even though the nodes are
  # up. We start the containers, merge the kubeconfig ourselves, then poll
  # node readiness via kubectl (bounded) — deterministic and hang-proof.
  k3d cluster create "$CLUSTER" --image "$K3S_IMAGE" --agents 1 -p "30080:30080@server:0"
  k3d kubeconfig merge "$CLUSTER" --kubeconfig-switch-context >/dev/null
  echo "waiting for the API server + nodes to become Ready"
  for i in $(seq 1 60); do
    if kubectl --context "k3d-$CLUSTER" wait --for=condition=Ready nodes --all --timeout=5s >/dev/null 2>&1; then
      echo "nodes Ready"
      break
    fi
    sleep 3
  done
fi
kubectl config use-context "k3d-$CLUSTER" >/dev/null 2>&1 || true

step "importing images into the cluster"
# App images plus the infra images the chart pulls (postgres/redpanda/redis).
# Importing the infra images from the local Docker cache avoids pulling large
# images from Docker Hub inside k3d's containerd, which is slow and flaky
# (TLS/i-o timeouts) on constrained networks and otherwise stalls the rollout.
# Includes k8s's pod-sandbox "pause" image: a fresh k3d node otherwise pulls
# it from Docker Hub for every pod, which stalls the whole rollout when the
# registry is unreachable. Importing it from the local cache makes the deploy
# work offline once the images have been fetched at least once.
INFRA_IMAGES=(postgres:16-alpine redpandadata/redpanda:v24.2.18 redis:7-alpine rancher/mirrored-pause:3.6)
for img in "${INFRA_IMAGES[@]}"; do
  docker image inspect "$img" >/dev/null 2>&1 || docker pull "$img"
done
IMAGES=("${INFRA_IMAGES[@]}")
for s in "${SERVICES[@]}"; do IMAGES+=("bank-core-$s:latest"); done
k3d image import "${IMAGES[@]}" -c "$CLUSTER"

step "helm upgrade --install"
# 10m: a cold k3d cluster on a small host loads images into containerd and
# schedules 10 pods behind the wait-for-infra init gate; 5m can be too tight.
helm upgrade --install bank-core "$CHART" --wait --timeout 10m

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
