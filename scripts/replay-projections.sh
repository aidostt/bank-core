#!/usr/bin/env bash
# replay-projections (M2 DoD): wipe the account balance projection, rewind
# the consumer group to offset 0 and prove the projection converges back to
# the ledger's truth (ADR-0008 — replayability is the point of a log).
set -euo pipefail

COMPOSE=(docker compose -f "$(dirname "$0")/../deploy/compose/docker-compose.yml")

echo "── replay-projections ──"

echo "1. stopping account-service and waiting for its group membership to lapse"
"${COMPOSE[@]}" stop account >/dev/null
# The group can only be deleted once it has no members; the coordinator only
# drops a stopped member after its session times out (~30s), so wait for the
# state to reach Empty/Dead before touching it.
for i in $(seq 1 40); do
  ST=$("${COMPOSE[@]}" exec -T redpanda rpk group describe account -X brokers=redpanda:9092 2>/dev/null | awk '/^STATE/{print $2}')
  [ "$ST" = "Empty" ] || [ "$ST" = "Dead" ] || [ -z "$ST" ] && break
  sleep 2
done
echo "   group state: ${ST:-gone}"

echo "2. truncating the balances projection"
"${COMPOSE[@]}" exec -T postgres psql -U account_user -d account_db -q \
  -c "TRUNCATE balances; DELETE FROM processed_messages WHERE consumer_group='account';"

echo "3. deleting consumer group 'account' (now empty)"
"${COMPOSE[@]}" exec -T redpanda rpk group delete account -X brokers=redpanda:9092 2>/dev/null || true

echo "4. starting account-service — a fresh group replays the journal from offset 0"
"${COMPOSE[@]}" start account >/dev/null

echo "5. waiting for convergence (projection == ledger truth)"
for i in $(seq 1 60); do
  sleep 2
  DIFF=$("${COMPOSE[@]}" exec -T postgres psql -U postgres -tA -c "
    SELECT count(*) FROM (
      SELECT b.account_id, b.balance FROM balances b
      EXCEPT
      SELECT a.external_account_id, ab.balance
      FROM account_balances ab JOIN ledger_accounts a ON a.id = ab.account_id
      WHERE a.type = 'customer'
    ) d;" -d account_db 2>/dev/null || echo "ERR")
  # cross-database compare is impossible in one query (ADR-0004) — fetch both sides
  LEDGER=$("${COMPOSE[@]}" exec -T postgres psql -U ledger_user -d ledger_db -tA -c \
    "SELECT COALESCE(string_agg(a.external_account_id || ':' || ab.balance, ',' ORDER BY a.external_account_id), '')
     FROM account_balances ab JOIN ledger_accounts a ON a.id = ab.account_id WHERE a.type='customer';")
  PROJ=$("${COMPOSE[@]}" exec -T postgres psql -U account_user -d account_db -tA -c \
    "SELECT COALESCE(string_agg(account_id || ':' || balance, ',' ORDER BY account_id), '') FROM balances;")
  if [ -n "$LEDGER" ] && [ "$LEDGER" = "$PROJ" ]; then
    echo "✔ converged after ~$((i*2))s: projection matches the ledger for every customer account"
    exit 0
  fi
done

echo "✘ projection did not converge to ledger truth" >&2
echo "ledger:     $LEDGER" >&2
echo "projection: $PROJ" >&2
exit 1
