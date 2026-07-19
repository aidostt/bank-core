#!/usr/bin/env bash
# bank-core end-to-end demo (M1): two users, KZT+USD accounts, top-ups,
# INTERNAL FX transfer, P2P transfer, balances before/after, idempotency
# replay, verify-ledger reconciliation.
set -euo pipefail

BASE="${GATEWAY_URL:-http://localhost:8080}"
JQ=jq

bold()  { printf '\033[1m%s\033[0m\n' "$*"; }
step()  { printf '\n\033[1;34m▶ %s\033[0m\n' "$*"; }
money() { # minor units → human units
  local v=$1
  printf '%d.%02d' $((v / 100)) $((v % 100 < 0 ? -(v % 100) : v % 100))
}

wait_ready() {
  step "Waiting for gateway at $BASE"
  for _ in $(seq 1 60); do
    if curl -fsS "$BASE/healthz" >/dev/null 2>&1; then
      # readiness of the whole path: login must reach identity (401 = alive)
      code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/v1/auth/login" \
               -H 'Content-Type: application/json' -d '{"email":"x@x.x","password":"xxxxxxxx"}' || true)
      if [ "$code" = "401" ] || [ "$code" = "200" ]; then
        echo "gateway ready"; return 0
      fi
    fi
    sleep 2
  done
  echo "gateway never became ready" >&2; exit 1
}

api() { # api METHOD PATH TOKEN [BODY] [EXTRA_HEADER...]
  local method=$1 path=$2 token=$3 body=${4:-}
  shift; shift; shift; [ $# -gt 0 ] && shift
  local args=(-sS --fail-with-body -X "$method" "$BASE$path" -H 'Content-Type: application/json')
  [ -n "$token" ] && args+=(-H "Authorization: Bearer $token")
  [ -n "$body" ] && args+=(-d "$body")
  for h in "$@"; do args+=(-H "$h"); done
  curl "${args[@]}"
}

balances() { # balances TOKEN LABEL
  local token=$1 label=$2
  bold "$label"
  api GET /v1/accounts "$token" | $JQ -r '.accounts[] | "  \(.number) [\(.currency)] \(.status): balance=\(.balance.balance // 0) held=\(.balance.held // 0) available=\(.balance.available // 0)"'
}

wait_ready

RUN_ID=$(date +%s)

step "Register two customers"
api POST /v1/auth/register '' "{\"email\":\"alice-$RUN_ID@demo.kz\",\"password\":\"correct-horse-1\",\"name\":\"Alice\",\"phone\":\"+7 700 000 0001\"}" | $JQ '{id, email}'
api POST /v1/auth/register '' "{\"email\":\"bob-$RUN_ID@demo.kz\",\"password\":\"correct-horse-2\",\"name\":\"Bob\",\"phone\":\"+7 700 000 0002\"}" | $JQ '{id, email}'

step "Login"
ALICE=$(api POST /v1/auth/login '' "{\"email\":\"alice-$RUN_ID@demo.kz\",\"password\":\"correct-horse-1\"}" | $JQ -r .access_token)
BOB=$(api POST /v1/auth/login '' "{\"email\":\"bob-$RUN_ID@demo.kz\",\"password\":\"correct-horse-2\"}" | $JQ -r .access_token)
echo "tokens acquired"

step "Open accounts: Alice KZT + USD, Bob KZT"
A_KZT=$(api POST /v1/accounts "$ALICE" '{"currency":"KZT"}'); echo "$A_KZT" | $JQ '{id, number, currency}'
A_USD=$(api POST /v1/accounts "$ALICE" '{"currency":"USD"}'); echo "$A_USD" | $JQ '{id, number, currency}'
B_KZT=$(api POST /v1/accounts "$BOB" '{"currency":"KZT"}');  echo "$B_KZT" | $JQ '{id, number, currency}'
A_KZT_ID=$(echo "$A_KZT" | $JQ -r .id)
A_USD_ID=$(echo "$A_USD" | $JQ -r .id)
B_KZT_ID=$(echo "$B_KZT" | $JQ -r .id)
B_KZT_NUM=$(echo "$B_KZT" | $JQ -r .number)

step "FX rates (seeded)"
api GET /v1/rates "$ALICE" | $JQ .

step "Top-ups: Alice 100,000.00 KZT + 1,000.00 USD; Bob 5,000.00 KZT"
api POST /v1/topups "$ALICE" "{\"account_id\":\"$A_KZT_ID\",\"minor_units\":10000000,\"currency\":\"KZT\"}" "Idempotency-Key: topup-alice-kzt-$RUN_ID" | $JQ '{id, state, amount}'
api POST /v1/topups "$ALICE" "{\"account_id\":\"$A_USD_ID\",\"minor_units\":100000,\"currency\":\"USD\"}" "Idempotency-Key: topup-alice-usd-$RUN_ID" | $JQ '{id, state, amount}'
api POST /v1/topups "$BOB"   "{\"account_id\":\"$B_KZT_ID\",\"minor_units\":500000,\"currency\":\"KZT\"}" "Idempotency-Key: topup-bob-kzt-$RUN_ID" | $JQ '{id, state, amount}'

balances "$ALICE" "Alice's balances BEFORE transfers"
balances "$BOB"   "Bob's balances BEFORE transfers"

step "INTERNAL FX transfer: Alice USD → Alice KZT, \$200.00 at the locked rate"
FX=$(api POST /v1/transfers "$ALICE" "{\"type\":\"INTERNAL\",\"from_account_id\":\"$A_USD_ID\",\"to_account_id\":\"$A_KZT_ID\",\"minor_units\":20000,\"currency\":\"USD\"}" "Idempotency-Key: fx-$RUN_ID")
echo "$FX" | $JQ '{id, state, amount, counter_amount, applied_rate}'

step "P2P transfer: Alice KZT → Bob (by account number), 15,000.00 KZT"
P2P=$(api POST /v1/transfers "$ALICE" "{\"type\":\"P2P\",\"from_account_id\":\"$A_KZT_ID\",\"to_account_number\":\"$B_KZT_NUM\",\"minor_units\":1500000,\"currency\":\"KZT\"}" "Idempotency-Key: p2p-$RUN_ID")
echo "$P2P" | $JQ '{id, state, amount}'
P2P_ID=$(echo "$P2P" | $JQ -r .id)

step "Idempotency replay: same P2P request + same Idempotency-Key → same transfer, no double spend"
REPLAY=$(api POST /v1/transfers "$ALICE" "{\"type\":\"P2P\",\"from_account_id\":\"$A_KZT_ID\",\"to_account_number\":\"$B_KZT_NUM\",\"minor_units\":1500000,\"currency\":\"KZT\"}" "Idempotency-Key: p2p-$RUN_ID")
REPLAY_ID=$(echo "$REPLAY" | $JQ -r .id)
if [ "$REPLAY_ID" = "$P2P_ID" ]; then
  echo "✔ replay returned the same transfer $P2P_ID"
else
  echo "✘ replay created a different transfer! $REPLAY_ID vs $P2P_ID" >&2; exit 1
fi

step "Transfer history (Alice)"
api GET /v1/transfers "$ALICE" | $JQ -r '.transfers[] | "  \(.type) \(.state) \(.amount.minor_units) \(.amount.currency) → \(.to_account_id)"'

balances "$ALICE" "Alice's balances AFTER transfers"
balances "$BOB"   "Bob's balances AFTER transfers"

step "verify-ledger: double-entry reconciliation"
bash "$(dirname "$0")/verify-ledger.sh"

bold ""
bold "Demo complete: FX + P2P moved money, idempotency held, the ledger reconciles."
