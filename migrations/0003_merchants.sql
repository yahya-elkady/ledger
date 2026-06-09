-- Migration 0003: payments-service core schema.
--
-- This is the "init" of the Stripe-like payments service (build.md Phase 2,
-- file 001_init.sql), renumbered to 0003 to coexist with the existing ledger
-- migrations (0001 accounts/entries, 0002 payments) in the same database.
-- There are no table-name collisions with the ledger schema.
--
-- Security: every mutable financial table carries deleted_at (soft delete —
-- financial records are never hard-deleted). audit_logs is append-only.
-- gen_random_uuid() is built into PostgreSQL 13+ (no pgcrypto needed).

-- merchants: one row per API user / business.
CREATE TABLE merchants (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  email         TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,          -- bcrypt, for dashboard login
  business_name TEXT NOT NULL,
  mode          TEXT NOT NULL DEFAULT 'test' CHECK (mode IN ('test','live')),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  deleted_at    TIMESTAMPTZ
);

-- customers: merchants' end-users.
CREATE TABLE customers (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  merchant_id UUID NOT NULL REFERENCES merchants(id),
  email       TEXT,
  name        TEXT,
  metadata    JSONB,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  deleted_at  TIMESTAMPTZ
);

-- payment_methods: tokenized cards / bank accounts (never raw card data).
CREATE TABLE payment_methods (
  id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  customer_id          UUID NOT NULL REFERENCES customers(id),
  merchant_id          UUID NOT NULL REFERENCES merchants(id),
  type                 TEXT NOT NULL CHECK (type IN ('card','bank_account')),
  processor            TEXT NOT NULL CHECK (processor IN ('stripe','plaid')),
  processor_method_id  TEXT NOT NULL,   -- Stripe PaymentMethod ID or Plaid token
  last4                TEXT,
  brand                TEXT,
  exp_month            SMALLINT,
  exp_year             SMALLINT,
  currency             CHAR(3),         -- ISO 4217
  is_default           BOOLEAN NOT NULL DEFAULT FALSE,
  created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  deleted_at           TIMESTAMPTZ
);

-- charges: one-time payments.
CREATE TABLE charges (
  id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  merchant_id         UUID NOT NULL REFERENCES merchants(id),
  customer_id         UUID REFERENCES customers(id),
  payment_method_id   UUID REFERENCES payment_methods(id),
  amount              BIGINT NOT NULL CHECK (amount > 0),  -- smallest currency unit (cents)
  currency            CHAR(3) NOT NULL,
  status              TEXT NOT NULL CHECK (status IN ('pending','succeeded','failed','refunded','partially_refunded')),
  processor           TEXT NOT NULL,
  processor_charge_id TEXT,
  idempotency_key     TEXT UNIQUE,
  mode                TEXT NOT NULL CHECK (mode IN ('test','live')),
  failure_code        TEXT,
  failure_message     TEXT,
  refunded_amount     BIGINT NOT NULL DEFAULT 0,
  metadata            JSONB,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  deleted_at          TIMESTAMPTZ
);

-- audit_logs: immutable append-only log of every mutation.
CREATE TABLE audit_logs (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  merchant_id UUID,
  actor_type  TEXT NOT NULL CHECK (actor_type IN ('api_key','jwt','system')),
  actor_id    TEXT NOT NULL,   -- api_key hash prefix or merchant_id
  action      TEXT NOT NULL,   -- e.g. 'charge.created', 'apikey.revoked'
  resource    TEXT NOT NULL,   -- table name
  resource_id UUID,
  diff        JSONB,           -- {before: {}, after: {}} — no PII
  ip_address  INET,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Enforce audit_logs as append-only for the application role: it may read and
-- insert history, but never rewrite or erase it. Guarded so the migration is
-- portable to databases where payments_app has not been provisioned (e.g. a
-- local dev DB applied as superuser).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'payments_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON audit_logs TO payments_app';
    EXECUTE 'REVOKE UPDATE, DELETE, TRUNCATE ON audit_logs FROM payments_app';
  END IF;
END $$;
