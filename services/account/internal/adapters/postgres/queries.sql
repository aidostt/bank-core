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
