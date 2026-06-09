-- Migration 0004: API keys (build.md Phase 2, file 002_api_keys.sql).
--
-- Only an HMAC-SHA256 hash of each key is stored — never the plaintext. The
-- plaintext is shown to the merchant exactly once at creation. key_prefix holds
-- the first chars of the plaintext for display/identification only.

CREATE TABLE api_keys (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  merchant_id  UUID NOT NULL REFERENCES merchants(id),
  name         TEXT NOT NULL,
  key_hash     TEXT NOT NULL UNIQUE,   -- HMAC-SHA256, hex-encoded
  key_prefix   TEXT NOT NULL,          -- first 8 chars of plaintext key (display)
  type         TEXT NOT NULL CHECK (type IN ('publishable','secret')),
  mode         TEXT NOT NULL CHECK (mode IN ('test','live')),
  scope        TEXT[] NOT NULL DEFAULT '{read}',  -- read | write | admin
  last_used_at TIMESTAMPTZ,
  expires_at   TIMESTAMPTZ,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  revoked_at   TIMESTAMPTZ
);
