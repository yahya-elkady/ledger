-- name: CreatePayment :one
INSERT INTO payments (id, amount, currency, status, idempotency_key, provider_ref, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetPayment :one
SELECT * FROM payments
WHERE id = $1;

-- name: GetPaymentByIdempotencyKey :one
SELECT * FROM payments
WHERE idempotency_key = $1;

-- name: UpdatePaymentStatus :one
UPDATE payments
SET status = $2, provider_ref = $3, updated_at = $4
WHERE id = $1
RETURNING *;
