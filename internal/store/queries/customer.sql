-- name: CreateCustomer :one
INSERT INTO customers (merchant_id, email, name, metadata)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetCustomer :one
SELECT * FROM customers
WHERE id = $1 AND merchant_id = $2 AND deleted_at IS NULL;

-- name: ListCustomersFirst :many
SELECT * FROM customers
WHERE merchant_id = $1 AND deleted_at IS NULL
ORDER BY created_at DESC, id DESC
LIMIT $2;

-- name: ListCustomersAfter :many
-- Keyset pagination: rows strictly "after" the (created_at, id) cursor under the
-- DESC ordering. Written as explicit boolean logic (rather than a row-value
-- comparison) so sqlc infers $2 as timestamptz and $3 as uuid.
SELECT * FROM customers
WHERE merchant_id = $1 AND deleted_at IS NULL
  AND (created_at < $2 OR (created_at = $2 AND id < $3))
ORDER BY created_at DESC, id DESC
LIMIT $4;

-- name: UpdateCustomer :one
UPDATE customers
SET email = $3, name = $4, metadata = $5, updated_at = NOW()
WHERE id = $1 AND merchant_id = $2 AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteCustomer :one
UPDATE customers
SET deleted_at = NOW()
WHERE id = $1 AND merchant_id = $2 AND deleted_at IS NULL
RETURNING id;
