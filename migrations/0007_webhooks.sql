-- Migration 0007: outbound webhook endpoints + delivery log
-- (build.md Phase 2, file 005_webhooks.sql).

-- Outbound webhook endpoints registered by merchants.
CREATE TABLE webhook_endpoints (
  id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  merchant_id         UUID NOT NULL REFERENCES merchants(id),
  url                 TEXT NOT NULL,
  events              TEXT[] NOT NULL,   -- e.g. ['charge.succeeded','subscription.canceled']
  signing_secret_hash TEXT NOT NULL,
  mode                TEXT NOT NULL CHECK (mode IN ('test','live')),
  is_active           BOOLEAN NOT NULL DEFAULT TRUE,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  deleted_at          TIMESTAMPTZ
);

-- Delivery log.
CREATE TABLE webhook_deliveries (
  id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  endpoint_id          UUID NOT NULL REFERENCES webhook_endpoints(id),
  event_type           TEXT NOT NULL,
  payload              JSONB NOT NULL,
  status               TEXT NOT NULL CHECK (status IN ('pending','delivered','failed')),
  attempt_count        SMALLINT NOT NULL DEFAULT 0,
  next_retry_at        TIMESTAMPTZ,
  last_response_status INT,
  last_response_body   TEXT,
  created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
