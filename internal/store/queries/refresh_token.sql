-- name: CreateRefreshToken :one
INSERT INTO refresh_tokens (merchant_id, token_hash, jti, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetRefreshTokenByHash :one
SELECT * FROM refresh_tokens
WHERE token_hash = $1;

-- name: RevokeRefreshTokenByJTI :one
UPDATE refresh_tokens
SET revoked_at = NOW()
WHERE jti = $1 AND revoked_at IS NULL
RETURNING *;

-- name: RevokeRefreshTokenByHash :one
UPDATE refresh_tokens
SET revoked_at = NOW()
WHERE token_hash = $1 AND revoked_at IS NULL
RETURNING *;
