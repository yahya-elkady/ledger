-- Migration 0001: initial ledger schema
-- accounts: the chart of accounts
-- entries:  the append-only double-entry ledger

CREATE TABLE accounts (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    currency   TEXT NOT NULL,
    type       TEXT NOT NULL
        CHECK (type IN ('asset', 'liability', 'equity', 'revenue', 'expense')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE entries (
    id             TEXT PRIMARY KEY,
    account_id     TEXT NOT NULL REFERENCES accounts(id),
    amount         BIGINT NOT NULL CHECK (amount > 0),  -- minor units, always positive
    direction      SMALLINT NOT NULL CHECK (direction IN (1, -1)),  -- 1 = debit, -1 = credit
    currency       TEXT NOT NULL,
    memo           TEXT NOT NULL DEFAULT '',
    transaction_id TEXT NOT NULL,  -- groups all entries from one EntrySet
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Fast lookup of all entries belonging to one transaction.
CREATE INDEX idx_entries_transaction_id ON entries (transaction_id);

-- Fast lookup of all entries for an account (balance calculation, statements).
CREATE INDEX idx_entries_account_id ON entries (account_id);