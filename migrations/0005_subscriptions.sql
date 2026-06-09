-- Migration 0005: plans + subscriptions (build.md Phase 2, file 003_subscriptions.sql).

CREATE TABLE plans (
  id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  merchant_id       UUID NOT NULL REFERENCES merchants(id),
  name              TEXT NOT NULL,
  amount            BIGINT NOT NULL CHECK (amount > 0),
  currency          CHAR(3) NOT NULL,
  interval          TEXT NOT NULL CHECK (interval IN ('day','week','month','year')),
  interval_count    SMALLINT NOT NULL DEFAULT 1,
  processor_plan_id TEXT,
  mode              TEXT NOT NULL CHECK (mode IN ('test','live')),
  created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  deleted_at        TIMESTAMPTZ
);

CREATE TABLE subscriptions (
  id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  merchant_id          UUID NOT NULL REFERENCES merchants(id),
  customer_id          UUID NOT NULL REFERENCES customers(id),
  plan_id              UUID NOT NULL REFERENCES plans(id),
  payment_method_id    UUID NOT NULL REFERENCES payment_methods(id),
  status               TEXT NOT NULL CHECK (status IN ('active','past_due','canceled','trialing','unpaid')),
  processor_sub_id     TEXT,
  current_period_start TIMESTAMPTZ,
  current_period_end   TIMESTAMPTZ,
  trial_end            TIMESTAMPTZ,
  canceled_at          TIMESTAMPTZ,
  mode                 TEXT NOT NULL CHECK (mode IN ('test','live')),
  metadata             JSONB,
  created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  deleted_at           TIMESTAMPTZ
);
