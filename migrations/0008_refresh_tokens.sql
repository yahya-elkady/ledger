-- Migration 0008: refresh tokens (build.md Phase 2, file 006_refresh_tokens.sql).
--
-- Refresh tokens are stored hashed (same HMAC pattern as API keys), never in
-- plaintext. jti matches the JWT's jti claim so a token can be revoked by id.

CREATE TABLE refresh_tokens (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  merchant_id UUID NOT NULL REFERENCES merchants(id),
  token_hash  TEXT NOT NULL UNIQUE,
  jti         TEXT NOT NULL UNIQUE,   -- matches JWT jti claim
  expires_at  TIMESTAMPTZ NOT NULL,
  revoked_at  TIMESTAMPTZ,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
