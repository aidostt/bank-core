#!/usr/bin/env bash
# dlq-inspect: print every DLQ topic and its messages with error headers
# (architecture §5 — DLQ handling is manual; replay runbook in docs).
set -euo pipefail

COMPOSE=(docker compose -f "$(dirname "$0")/../deploy/compose/docker-compose.yml")
RPK=("${COMPOSE[@]}" exec -T redpanda rpk)

echo "── DLQ topics ──"
topics=$("${RPK[@]}" topic list 2>/dev/null | awk 'NR>1 {print $1}' | grep '\.dlq$' || true)
if [ -z "$topics" ]; then
  echo "no DLQ topics exist — nothing has been dead-lettered."
  exit 0
fi

for t in $topics; do
  echo
  echo "═══ $t ═══"
  "${RPK[@]}" topic consume "$t" --num 50 --offset start --format json 2>/dev/null \
    | head -200 || echo "  (empty)"
done
