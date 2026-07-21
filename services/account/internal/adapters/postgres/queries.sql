-- name: UpsertCustomer :one
INSERT INTO customers (user_id)
VALUES ($1)
ON CONFLICT (user_id) DO UPDATE SET user_id = EXCLUDED.user_id
RETURNING *;

-- name: GetCustomerByUserID :one
SELECT * FROM customers WHERE user_id = $1;

-- name: CreateAccount :one
INSERT INTO accounts (id, customer_id, number, currency)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetAccountByID :one
SELECT a.*, c.user_id, c.tier
FROM accounts a
JOIN customers c ON c.id = a.customer_id
WHERE a.id = $1;

-- name: GetAccountByNumber :one
SELECT a.*, c.user_id, c.tier
FROM accounts a
JOIN customers c ON c.id = a.customer_id
WHERE a.number = $1;

-- name: ListAccountsByUser :many
SELECT a.*, c.user_id, c.tier
FROM accounts a
JOIN customers c ON c.id = a.customer_id
WHERE c.user_id = $1
ORDER BY a.opened_at;

-- name: UpdateAccountStatus :one
UPDATE accounts SET status = $2 WHERE id = $1 RETURNING *;

-- name: UpsertBalanceGuarded :execrows
-- Version-guarded projection upsert (ADR-0006): reordered events are
-- ignored because only a strictly newer version may overwrite.
INSERT INTO balances (account_id, balance, version, as_of)
VALUES ($1, $2, $3, $4)
ON CONFLICT (account_id) DO UPDATE
SET balance = EXCLUDED.balance,
    version = EXCLUDED.version,
    as_of   = EXCLUDED.as_of
WHERE balances.version < EXCLUDED.version;

-- name: GetBalancesForAccounts :many
SELECT * FROM balances WHERE account_id = ANY($1::uuid[]);

-- name: GetAccountStatus :one
SELECT status FROM accounts WHERE id = $1;
