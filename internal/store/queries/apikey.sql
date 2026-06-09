-- name: CreateAPIKey :one
INSERT INTO api_keys (merchant_id, name, key_hash, key_prefix, type, mode, scope, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetAPIKeyByHash :one
SELECT * FROM api_keys
WHERE key_hash = $1;

-- name: GetAPIKeyByID :one
SELECT * FROM api_keys
WHERE id = $1;

-- name: ListAPIKeysByMerchant :many
SELECT * FROM api_keys
WHERE merchant_id = $1 AND revoked_at IS NULL
ORDER BY created_at DESC;

-- name: RevokeAPIKey :one
UPDATE api_keys
SET revoked_at = NOW()
WHERE id = $1 AND merchant_id = $2 AND revoked_at IS NULL
RETURNING *;

-- name: TouchAPIKeyLastUsed :exec
UPDATE api_keys
SET last_used_at = NOW()
WHERE id = $1;
