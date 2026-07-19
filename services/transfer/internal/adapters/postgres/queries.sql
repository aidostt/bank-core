-- name: TryInsertIdempotencyKey :execrows
INSERT INTO idempotency_keys (customer_id, key, transfer_id, request_hash)
VALUES ($1, $2, $3, $4)
ON CONFLICT (customer_id, key) DO NOTHING;

-- name: GetIdempotencyKey :one
SELECT * FROM idempotency_keys WHERE customer_id = $1 AND key = $2;

-- name: DeleteExpiredIdempotencyKeys :execrows
DELETE FROM idempotency_keys WHERE created_at < $1;

-- name: InsertTransfer :exec
INSERT INTO transfers (id, type, state, customer_id, from_account_id, to_account_id,
                       to_account_number, amount, currency)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: GetTransfer :one
SELECT * FROM transfers WHERE id = $1;

-- name: UpdateTransferGuarded :execrows
UPDATE transfers
SET state               = $2,
    to_account_id       = $3,
    counter_amount      = $4,
    counter_currency    = $5,
    applied_rate_micros = $6,
    rate_pair           = $7,
    reason              = $8,
    hold_id             = $9,
    updated_at          = now()
WHERE id = $1 AND state = sqlc.arg(expected_state);

-- name: AppendTransferEvent :exec
INSERT INTO transfer_events (transfer_id, from_state, to_state, detail)
VALUES ($1, $2, $3, $4);

-- name: ListTransfersByCustomer :many
SELECT * FROM transfers
WHERE customer_id = $1
  AND (created_at, id) < (sqlc.arg(cursor_time)::timestamptz, sqlc.arg(cursor_id)::uuid)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit);

-- name: GetLatestRate :one
SELECT * FROM rates
WHERE pair = $1 AND valid_from <= $2
ORDER BY valid_from DESC
LIMIT 1;

-- name: ListLatestRates :many
SELECT DISTINCT ON (pair) *
FROM rates
WHERE valid_from <= $1
ORDER BY pair, valid_from DESC;

-- name: GetLimit :one
SELECT * FROM limits WHERE tier = $1 AND currency = $2;

-- name: SumDailyOutgoing :one
SELECT COALESCE(sum(amount), 0)::bigint AS total
FROM transfers
WHERE customer_id = $1
  AND currency = $2
  AND type IN ('INTERNAL', 'P2P')
  AND state <> 'FAILED'
  AND created_at >= $3;

-- name: ClaimStuck :many
WITH stuck AS (
    SELECT id FROM transfers
    WHERE state = ANY(sqlc.arg(states)::text[])
      AND updated_at < now() - sqlc.arg(stale_after)::interval
    ORDER BY updated_at
    LIMIT sqlc.arg(claim_limit)
    FOR UPDATE SKIP LOCKED
)
UPDATE transfers t
SET recovery_attempts = t.recovery_attempts + 1, updated_at = now()
FROM stuck
WHERE t.id = stuck.id
RETURNING t.id;
