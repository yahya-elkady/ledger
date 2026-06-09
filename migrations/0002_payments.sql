-- Migration 0002: payment layer
--
-- payments:        the payment entity persisted for status tracking and
--                  idempotency. The UNIQUE constraint on idempotency_key is
--                  the database-level backstop for the service's idempotency
--                  check (defense-in-depth against a race).
-- system accounts: the seeded accounts the payment orchestration posts against.
--                  Seeding here (rather than at demo startup) is the durable
--                  choice: the accounts exist as soon as the schema is migrated.

CREATE TABLE payments (
    id              TEXT PRIMARY KEY,
    amount          BIGINT NOT NULL CHECK (amount > 0),  -- minor units, positive
    currency        TEXT NOT NULL,
    status          TEXT NOT NULL
        CHECK (status IN ('pending', 'authorized', 'captured', 'voided', 'failed')),
    idempotency_key TEXT NOT NULL UNIQUE,                -- UNIQUE => idempotency backstop
    provider_ref    TEXT NOT NULL DEFAULT '',            -- set on authorize
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Seed the system accounts the orchestration debits and credits.
--   cash-in-transit    (asset)     — funds in flight, reserved on authorize
--   authorization-hold (liability) — funds held, not yet earned
--   settled-funds      (revenue)   — funds recognized on capture
-- ON CONFLICT DO NOTHING keeps the migration idempotent if re-run.
INSERT INTO accounts (id, name, currency, type) VALUES
    ('sys-cash-in-transit',    'Cash In Transit',    'USD', 'asset'),
    ('sys-authorization-hold', 'Authorization Hold', 'USD', 'liability'),
    ('sys-settled-funds',      'Settled Funds',      'USD', 'revenue')
ON CONFLICT (id) DO NOTHING;
