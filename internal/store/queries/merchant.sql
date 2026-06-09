-- name: CreateMerchant :one
INSERT INTO merchants (email, password_hash, business_name, mode)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetMerchantByEmail :one
SELECT * FROM merchants
WHERE email = $1 AND deleted_at IS NULL;

-- name: GetMerchantByID :one
SELECT * FROM merchants
WHERE id = $1 AND deleted_at IS NULL;
