#!/usr/bin/env bash
# Capture the two provisioned Grafana dashboards to docs/img via the
# grafana-image-renderer sidecar (prompts/M2.md DoD). Run after make
# up-observability and some traffic (make demo / make load).
set -euo pipefail

OUT="$(dirname "$0")/../docs/img"
mkdir -p "$OUT"
GRAFANA="${GRAFANA_URL:-http://localhost:3000}"

render() { # render UID FILE
  local uid=$1 file=$2
  echo "rendering $uid → $file"
  curl -fsS "$GRAFANA/render/d/$uid/?orgId=1&from=now-15m&to=now&width=1400&height=900&kiosk" \
    -o "$OUT/$file"
}

render platform-red platform-red.png
render money-flow money-flow.png
echo "✔ dashboards saved to docs/img/"
echo "  (Jaeger trace screenshot is captured manually from http://localhost:16686)"
