-- name: CreateUser :one
INSERT INTO users (email, password_hash, name, phone)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: AddRole :exec
INSERT INTO roles (user_id, role) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: GetRoles :many
SELECT role FROM roles WHERE user_id = $1 ORDER BY role;

-- name: CreateSession :one
INSERT INTO sessions (user_id, family_id, refresh_hash, rotated_from, expires_at, ip, user_agent)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetSessionByRefreshHash :one
SELECT * FROM sessions WHERE refresh_hash = $1;

-- name: GetSessionByID :one
SELECT * FROM sessions WHERE id = $1;

-- name: MarkSessionRotated :execrows
UPDATE sessions
SET revoked_at = now(), rotated_to = $2
WHERE id = $1 AND revoked_at IS NULL;

-- name: RevokeSessionFamily :exec
UPDATE sessions
SET revoked_at = now()
WHERE family_id = $1 AND revoked_at IS NULL;

-- name: ListActiveSessions :many
SELECT * FROM sessions
WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()
ORDER BY created_at DESC;
