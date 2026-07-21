#!/usr/bin/env bash
# make chaos (prompts/M3.md §2): start a transfer burst, cut the
# transfer↔ledger link via toxiproxy for 10s mid-burst, restore, wait for
# the recovery worker, then assert every transfer is terminal and the
# ledger reconciles. Prints a narrative log for the README.
set -euo pipefail

BASE="${GATEWAY_URL:-http://localhost:8080}"
TOXI="${TOXIPROXY_URL:-http://localhost:8474}"
COMPOSE=(docker compose -f "$(dirname "$0")/../deploy/compose/docker-compose.yml")
JQ=jq

step() { printf '\n\033[1;35m⚡ %s\033[0m\n' "$*"; }

RUN_ID=$(date +%s)

step "chaos run $RUN_ID: preparing an actor with funded account"
curl -fsS -X POST "$BASE/v1/auth/register" -H 'Content-Type: application/json' \
  -d "{\"email\":\"chaos-$RUN_ID@demo.kz\",\"password\":\"chaos-pass-1\",\"name\":\"Chaos\"}" >/dev/null
TOKEN=$(curl -fsS -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d "{\"email\":\"chaos-$RUN_ID@demo.kz\",\"password\":\"chaos-pass-1\"}" | $JQ -r .access_token)
FROM=$(curl -fsS -X POST "$BASE/v1/accounts" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"currency":"KZT"}' | $JQ -r .id)
TO_JSON=$(curl -fsS -X POST "$BASE/v1/accounts" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"currency":"KZT"}')
TO_NUMBER=$(echo "$TO_JSON" | $JQ -r .number)
curl -fsS -X POST "$BASE/v1/topups" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -H "Idempotency-Key: chaos-topup-$RUN_ID" \
  -d "{\"account_id\":\"$FROM\",\"minor_units\":10000000,\"currency\":\"KZT\"}" >/dev/null
echo "actor ready: $FROM → $TO_NUMBER"

step "starting a 30-transfer burst in the background (~2/s, respecting the limiter)"
IDS_FILE=$(mktemp)
(
  for i in $(seq 1 30); do
    RESP=$(curl -sS -X POST "$BASE/v1/transfers" -H "Authorization: Bearer $TOKEN" \
      -H 'Content-Type: application/json' -H "Idempotency-Key: chaos-$RUN_ID-$i" \
      -d "{\"type\":\"INTERNAL\",\"from_account_id\":\"$FROM\",\"to_account_id\":\"$(echo "$TO_JSON" | $JQ -r .id)\",\"minor_units\":1000,\"currency\":\"KZT\"}" || true)
    ID=$(echo "$RESP" | $JQ -r '.id // empty' 2>/dev/null || true)
    STATE=$(echo "$RESP" | $JQ -r '.state // .code // "ERR"' 2>/dev/null || echo ERR)
    [ -n "$ID" ] && echo "$ID" >> "$IDS_FILE"
    echo "  burst[$i]: state=$STATE"
    sleep 0.6
  done
) &
BURST_PID=$!

sleep 4
step "CUTTING transfer↔ledger (toxiproxy ledger_grpc disabled) for 10s"
curl -fsS -X POST "$TOXI/proxies/ledger_grpc" -d '{"enabled": false}' >/dev/null
sleep 10
step "RESTORING the link"
curl -fsS -X POST "$TOXI/proxies/ledger_grpc" -d '{"enabled": true}' >/dev/null

wait $BURST_PID || true
TOTAL=$(wc -l < "$IDS_FILE" | tr -d ' ')
echo "burst finished: $TOTAL transfers accepted by the gateway"

step "waiting for the recovery worker to resolve every in-flight saga"
DEADLINE=$(( $(date +%s) + 120 ))
while :; do
  NONTERMINAL=0
  COMPLETED=0
  FAILED=0
  while read -r id; do
    S=$(curl -sS "$BASE/v1/transfers/$id" -H "Authorization: Bearer $TOKEN" | $JQ -r .state)
    case "$S" in
      COMPLETED) COMPLETED=$((COMPLETED+1));;
      FAILED)    FAILED=$((FAILED+1));;
      *)         NONTERMINAL=$((NONTERMINAL+1));;
    esac
  done < "$IDS_FILE"
  echo "  terminal: $COMPLETED completed, $FAILED failed; in-flight: $NONTERMINAL"
  if [ "$NONTERMINAL" -eq 0 ]; then break; fi
  if [ "$(date +%s)" -gt "$DEADLINE" ]; then
    echo "✘ transfers stuck non-terminal after 120s" >&2; exit 1
  fi
  sleep 5
done
echo "✔ every accepted transfer reached a terminal state (none stuck)"

step "verify-ledger: money conserved through the outage"
bash "$(dirname "$0")/verify-ledger.sh"

step "chaos run complete — the saga survived a 10s ledger outage"
