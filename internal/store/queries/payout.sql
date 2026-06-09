-- name: CreateBankAccount :one
INSERT INTO bank_accounts (merchant_id, processor, processor_acct_id, last4, bank_name, currency, is_default)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ListBankAccounts :many
SELECT * FROM bank_accounts
WHERE merchant_id = $1 AND deleted_at IS NULL
ORDER BY created_at DESC;

-- name: GetBankAccount :one
SELECT * FROM bank_accounts
WHERE id = $1 AND merchant_id = $2 AND deleted_at IS NULL;

-- name: SoftDeleteBankAccount :one
UPDATE bank_accounts
SET deleted_at = NOW()
WHERE id = $1 AND merchant_id = $2 AND deleted_at IS NULL
RETURNING id;

-- name: CreatePayout :one
INSERT INTO payouts (
  merchant_id, bank_account_id, amount, currency, status, processor,
  processor_payout_id, idempotency_key, mode, arrival_date
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: GetPayout :one
SELECT * FROM payouts
WHERE id = $1 AND merchant_id = $2 AND mode = $3 AND deleted_at IS NULL;

-- name: ListPayouts :many
SELECT * FROM payouts
WHERE merchant_id = $1
  AND mode = $2
  AND deleted_at IS NULL
  AND (
    sqlc.narg('cursor_created')::timestamptz IS NULL
    OR created_at < sqlc.narg('cursor_created')
    OR (created_at = sqlc.narg('cursor_created') AND id < sqlc.narg('cursor_id'))
  )
ORDER BY created_at DESC, id DESC
LIMIT $3;

-- name: UpdatePayoutStatusByProcessorID :one
UPDATE payouts
SET status = $2, failure_message = $3, updated_at = NOW()
WHERE processor_payout_id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: CountPendingPayouts :one
SELECT COUNT(*) FROM payouts
WHERE merchant_id = $1 AND mode = $2 AND status IN ('pending','in_transit') AND deleted_at IS NULL;
