-- name: CreatePlan :one
INSERT INTO plans (merchant_id, name, amount, currency, interval, interval_count, processor_plan_id, mode)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: ListPlans :many
SELECT * FROM plans
WHERE merchant_id = $1 AND mode = $2 AND deleted_at IS NULL
ORDER BY created_at DESC;

-- name: GetPlan :one
SELECT * FROM plans
WHERE id = $1 AND merchant_id = $2 AND mode = $3 AND deleted_at IS NULL;

-- name: SoftDeletePlan :one
UPDATE plans
SET deleted_at = NOW()
WHERE id = $1 AND merchant_id = $2 AND deleted_at IS NULL
RETURNING id;

-- name: CreateSubscription :one
INSERT INTO subscriptions (
  merchant_id, customer_id, plan_id, payment_method_id, status, processor_sub_id,
  current_period_start, current_period_end, trial_end, mode, metadata
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: GetSubscription :one
SELECT * FROM subscriptions
WHERE id = $1 AND merchant_id = $2 AND mode = $3 AND deleted_at IS NULL;

-- name: ListSubscriptions :many
SELECT * FROM subscriptions
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

-- name: SetSubscriptionStatus :one
UPDATE subscriptions
SET status = $3,
    canceled_at = CASE WHEN sqlc.arg('set_canceled')::bool THEN NOW() ELSE canceled_at END,
    updated_at = NOW()
WHERE id = $1 AND merchant_id = $2 AND deleted_at IS NULL
RETURNING *;

-- name: CountActiveSubscriptions :one
SELECT COUNT(*) FROM subscriptions
WHERE merchant_id = $1 AND mode = $2 AND status = 'active' AND deleted_at IS NULL;

-- name: UpdateSubscriptionStatusByProcessorID :one
UPDATE subscriptions
SET status = $2,
    canceled_at = CASE WHEN $2 = 'canceled' THEN NOW() ELSE canceled_at END,
    updated_at = NOW()
WHERE processor_sub_id = $1 AND deleted_at IS NULL
RETURNING *;
