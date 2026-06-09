-- Migration 0006: bank_accounts + payouts (build.md Phase 2, file 004_payouts.sql).

CREATE TABLE bank_accounts (
  id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  merchant_id       UUID NOT NULL REFERENCES merchants(id),
  processor         TEXT NOT NULL CHECK (processor IN ('stripe','plaid')),
  processor_acct_id TEXT NOT NULL,
  last4             TEXT,
  bank_name         TEXT,
  currency          CHAR(3),
  is_default        BOOLEAN NOT NULL DEFAULT FALSE,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  deleted_at        TIMESTAMPTZ
);

CREATE TABLE payouts (
  id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  merchant_id         UUID NOT NULL REFERENCES merchants(id),
  bank_account_id     UUID NOT NULL REFERENCES bank_accounts(id),
  amount              BIGINT NOT NULL CHECK (amount > 0),
  currency            CHAR(3) NOT NULL,
  status              TEXT NOT NULL CHECK (status IN ('pending','in_transit','paid','failed','canceled')),
  processor           TEXT NOT NULL,
  processor_payout_id TEXT,
  idempotency_key     TEXT UNIQUE,
  mode                TEXT NOT NULL CHECK (mode IN ('test','live')),
  failure_message     TEXT,
  arrival_date        DATE,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  deleted_at          TIMESTAMPTZ
);
