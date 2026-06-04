-- name: CreateAccount :one
INSERT INTO accounts (id, name, currency, type, created_at)
VALUES ($1, $2, $3, $4, now())
RETURNING *;

-- name: GetAccount :one
SELECT * FROM accounts
WHERE id = $1;

-- name: ListAccounts :many
SELECT * FROM accounts
ORDER BY created_at;

-- name: CreateEntry :one
INSERT INTO entries (id, account_id, amount, direction, currency, memo, transaction_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, now())
RETURNING *;

-- name: GetEntriesByTransaction :many
SELECT * FROM entries
WHERE transaction_id = $1
ORDER BY created_at;

-- name: GetEntriesByAccount :many
SELECT * FROM entries
WHERE account_id = $1
ORDER BY created_at;