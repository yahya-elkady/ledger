-- name: ChargeStatsByMode :one
-- Aggregate charge volume for a merchant+mode: total count, succeeded count,
-- and gross succeeded volume (minor units).
SELECT
  COUNT(*)                                                        AS total_count,
  COUNT(*) FILTER (WHERE status = 'succeeded')                    AS succeeded_count,
  COALESCE(SUM(amount) FILTER (WHERE status = 'succeeded'), 0)::bigint AS succeeded_volume,
  COUNT(*) FILTER (WHERE status = 'failed')                       AS failed_count
FROM charges
WHERE merchant_id = $1 AND mode = $2 AND deleted_at IS NULL;

-- name: RecentFailedCharges :many
SELECT * FROM charges
WHERE merchant_id = $1 AND mode = $2 AND status = 'failed' AND deleted_at IS NULL
ORDER BY created_at DESC
LIMIT $3;
