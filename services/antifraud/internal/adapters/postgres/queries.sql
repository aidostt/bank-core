-- name: ListEnabledRules :many
SELECT * FROM rules WHERE enabled ORDER BY id;

-- name: GetStats :one
SELECT * FROM customer_stats WHERE customer_id = $1 AND currency = $2;

-- name: UpsertStats :exec
INSERT INTO customer_stats (customer_id, currency, day, day_out_sum, win5m_start, win5m_count, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, now())
ON CONFLICT (customer_id, currency) DO UPDATE
SET day         = EXCLUDED.day,
    day_out_sum = EXCLUDED.day_out_sum,
    win5m_start = EXCLUDED.win5m_start,
    win5m_count = EXCLUDED.win5m_count,
    updated_at  = now();

-- name: InsertAlert :exec
INSERT INTO alerts (id, customer_id, transfer_id, rule_id, severity, details)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: TryInsertBeneficiary :execrows
INSERT INTO beneficiaries (customer_id, counterparty_account)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: CountAlerts :one
SELECT count(*) FROM alerts WHERE customer_id = $1;
