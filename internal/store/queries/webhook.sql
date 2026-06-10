-- Outbound webhook endpoints + delivery log (build.md Phase 7).
-- All queries are parameterized; the delivery loop is driven by status +
-- next_retry_at, so a single UPDATE shape records every attempt outcome.

-- name: ListActiveWebhookEndpoints :many
SELECT id, url
FROM webhook_endpoints
WHERE merchant_id = $1
  AND mode = $2
  AND is_active
  AND deleted_at IS NULL
  AND sqlc.arg(event_type)::text = ANY(events);

-- name: CreateWebhookDelivery :one
INSERT INTO webhook_deliveries (endpoint_id, event_type, payload, status, next_retry_at)
VALUES ($1, $2, $3, 'pending', NOW())
RETURNING id;

-- name: DueWebhookDeliveries :many
SELECT d.id, d.endpoint_id, d.event_type, d.payload, d.attempt_count, e.url
FROM webhook_deliveries d
JOIN webhook_endpoints e ON e.id = d.endpoint_id
WHERE d.status = 'pending'
  AND d.next_retry_at <= NOW()
ORDER BY d.next_retry_at
LIMIT $1;

-- name: FinishWebhookDeliveryAttempt :exec
UPDATE webhook_deliveries
SET status               = $2,
    attempt_count        = attempt_count + 1,
    next_retry_at        = $3,
    last_response_status = $4,
    last_response_body   = $5,
    updated_at           = NOW()
WHERE id = $1;
