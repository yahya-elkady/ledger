-- name: CreateAuditLog :one
INSERT INTO audit_logs (merchant_id, actor_type, actor_id, action, resource, resource_id, diff, ip_address)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id;
