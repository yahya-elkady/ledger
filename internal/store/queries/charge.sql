-- name: CreateCharge :one
INSERT INTO charges (
  merchant_id, customer_id, payment_method_id, amount, currency, status,
  processor, processor_charge_id, idempotency_key, mode, failure_code,
  failure_message, metadata
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: GetCharge :one
SELECT * FROM charges
WHERE id = $1 AND merchant_id = $2 AND mode = $3 AND deleted_at IS NULL;

-- name: GetChargeByProcessorID :one
SELECT * FROM charges
WHERE processor_charge_id = $1 AND deleted_at IS NULL;

-- name: ListCharges :many
-- Mode-isolated, merchant-scoped, newest first. Optional status filter and
-- keyset cursor are passed as nullable args (sqlc.narg); when NULL they are
-- no-ops. Keyset written as explicit booleans so sqlc types the params.
SELECT * FROM charges
WHERE merchant_id = $1
  AND mode = $2
  AND deleted_at IS NULL
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (
    sqlc.narg('cursor_created')::timestamptz IS NULL
    OR created_at < sqlc.narg('cursor_created')
    OR (created_at = sqlc.narg('cursor_created') AND id < sqlc.narg('cursor_id'))
  )
ORDER BY created_at DESC, id DESC
LIMIT $3;

-- name: UpdateChargeStatusByProcessorID :one
UPDATE charges
SET status = $2, failure_code = $3, failure_message = $4, updated_at = NOW()
WHERE processor_charge_id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: UpdateChargeRefund :one
UPDATE charges
SET refunded_amount = $3, status = $4, updated_at = NOW()
WHERE id = $1 AND merchant_id = $2 AND deleted_at IS NULL
RETURNING *;
