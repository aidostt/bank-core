#!/usr/bin/env bash
# verify-ledger: reconciliation queries straight against ledger_db
# (ledger doc invariants 1, 3, 4). Exits non-zero on any violation.
set -euo pipefail

PSQL=(docker compose -f "$(dirname "$0")/../deploy/compose/docker-compose.yml" exec -T postgres psql -U ledger_user -d ledger_db -tA)

fail=0

check() { # check LABEL QUERY  — query must return 0
  local label=$1 query=$2
  local bad
  bad=$("${PSQL[@]}" -c "$query")
  if [ "$bad" = "0" ]; then
    echo "✔ $label"
  else
    echo "✘ $label: $bad violation(s)" >&2
    fail=1
  fi
}

echo "── verify-ledger ──"

check "every journal entry sums to zero per currency (invariant 1)" \
  "SELECT count(*) FROM (SELECT entry_id FROM postings GROUP BY entry_id, currency HAVING sum(amount) <> 0) bad;"

check "materialized balance ≡ Σ postings for every account (invariant 4)" \
  "SELECT count(*) FROM account_balances b
   WHERE b.balance <> COALESCE((SELECT sum(p.amount) FROM postings p WHERE p.account_id = b.account_id), 0);"

check "no negative available balance on customer accounts (invariant 3)" \
  "SELECT count(*) FROM account_balances b
   JOIN ledger_accounts a ON a.id = b.account_id
   WHERE a.type = 'customer' AND b.balance - b.held < 0;"

check "no negative held amounts" \
  "SELECT count(*) FROM account_balances WHERE held < 0;"

check "total money in the system is conserved (Σ all postings = 0)" \
  "SELECT CASE WHEN COALESCE(sum(amount), 0) = 0 THEN 0 ELSE 1 END FROM postings;"

if [ "$fail" -ne 0 ]; then
  echo "verify-ledger: FAILED" >&2
  exit 1
fi
echo "verify-ledger: PASS — the books balance."
