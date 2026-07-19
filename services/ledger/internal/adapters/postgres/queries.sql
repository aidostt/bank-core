-- name: GetAccountByID :one
SELECT * FROM ledger_accounts WHERE id = $1;

-- name: GetAccountByExternalID :one
SELECT * FROM ledger_accounts WHERE external_account_id = $1;

-- name: GetAccountByInternalCode :one
SELECT * FROM ledger_accounts WHERE internal_code = $1;

-- name: InsertCustomerAccount :one
INSERT INTO ledger_accounts (external_account_id, type, currency)
VALUES ($1, 'customer', $2)
ON CONFLICT (external_account_id) DO NOTHING
RETURNING *;

-- name: InsertBalanceRow :exec
INSERT INTO account_balances (account_id, currency)
VALUES ($1, $2)
ON CONFLICT (account_id) DO NOTHING;

-- name: LockBalance :one
SELECT * FROM account_balances WHERE account_id = $1 FOR UPDATE;

-- name: UpdateBalance :exec
UPDATE account_balances
SET balance = $2, held = $3, version = $4, updated_at = now()
WHERE account_id = $1;

-- name: GetBalancesByIDs :many
SELECT b.account_id, b.currency, b.balance, b.held, b.version, b.updated_at,
       a.external_account_id, a.internal_code
FROM account_balances b
JOIN ledger_accounts a ON a.id = b.account_id
WHERE b.account_id = ANY($1::uuid[]);

-- name: GetEntryRef :one
SELECT * FROM journal_entry_refs WHERE reference_type = $1 AND reference_id = $2;

-- name: InsertEntry :exec
INSERT INTO journal_entries (id, reference_type, reference_id, occurred_at)
VALUES ($1, $2, $3, $4);

-- name: InsertEntryRef :exec
INSERT INTO journal_entry_refs (reference_type, reference_id, entry_id, occurred_at)
VALUES ($1, $2, $3, $4);

-- name: InsertPosting :exec
INSERT INTO postings (id, entry_id, account_id, amount, currency, occurred_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: GetEntry :one
SELECT * FROM journal_entries WHERE id = $1 AND occurred_at = $2;

-- name: GetEntryPostings :many
SELECT p.id, p.entry_id, p.account_id, p.amount, p.currency, p.occurred_at,
       a.external_account_id
FROM postings p
JOIN ledger_accounts a ON a.id = p.account_id
WHERE p.entry_id = $1 AND p.occurred_at = $2
ORDER BY p.id;

-- name: ListPostingsPage :many
SELECT p.id, p.entry_id, p.account_id, p.amount, p.currency, p.occurred_at,
       a.external_account_id
FROM postings p
JOIN ledger_accounts a ON a.id = p.account_id
WHERE p.account_id = $1
  AND p.occurred_at >= sqlc.arg(from_time)
  AND p.occurred_at < sqlc.arg(to_time)
  AND (p.occurred_at, p.id) < (sqlc.arg(cursor_time)::timestamptz, sqlc.arg(cursor_id)::uuid)
ORDER BY p.occurred_at DESC, p.id DESC
LIMIT sqlc.arg(page_limit);

-- name: GetHoldByReference :one
SELECT * FROM holds WHERE reference_type = $1 AND reference_id = $2;

-- name: LockHoldByID :one
SELECT * FROM holds WHERE id = $1 FOR UPDATE;

-- name: InsertHold :exec
INSERT INTO holds (id, account_id, amount, currency, reference_type, reference_id, status, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: SetHoldStatus :exec
UPDATE holds SET status = $2, updated_at = now() WHERE id = $1;

-- name: ListExpiredHolds :many
SELECT * FROM holds
WHERE status = 'active' AND expires_at < now()
ORDER BY expires_at
LIMIT $1;

-- name: SumPostingsForAccount :one
SELECT COALESCE(sum(amount), 0)::bigint AS total
FROM postings
WHERE account_id = $1;
