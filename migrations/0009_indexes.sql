-- Migration 0009: performance indexes (build.md Phase 2, file 007_indexes.sql).
--
-- Partial indexes (WHERE deleted_at IS NULL / revoked_at IS NULL) keep the
-- common "active rows only" lookups small. The webhook delivery index targets
-- the dispatcher's poll for due retries.

CREATE INDEX idx_charges_merchant_id ON charges(merchant_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_charges_customer_id ON charges(customer_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_charges_idempotency_key ON charges(idempotency_key);
CREATE INDEX idx_subscriptions_merchant_id ON subscriptions(merchant_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_subscriptions_customer_id ON subscriptions(customer_id);
CREATE INDEX idx_api_keys_merchant_id ON api_keys(merchant_id) WHERE revoked_at IS NULL;
CREATE INDEX idx_audit_logs_merchant_id ON audit_logs(merchant_id);
CREATE INDEX idx_audit_logs_resource ON audit_logs(resource, resource_id);
CREATE INDEX idx_webhook_deliveries_status ON webhook_deliveries(status, next_retry_at) WHERE status = 'pending';
