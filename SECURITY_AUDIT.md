# Security Audit — Phase 14 Final Review & Hardening

**Date:** 2026-06-10 · **Scope:** entire repository at Phase 13 completion · **Stance:** pre-production audit of a payments service ("would I let this touch real funds").

Every finding below was either **fixed in this pass** (code referenced) or is listed under **Residual risk** with a reason. Verification state at the end of the pass: `go build ./...` clean, `go vet ./...` clean (including `-tags=integration`), `golangci-lint run` **0 issues**, all 13 test packages green.

---

## Section A — Automated secret & injection sweep

| Check | Result |
|---|---|
| `sk_live\|sk_test\|whsec_\|pk_live\|pk_test` in `*.go` | **Clean.** All hits are test fixtures with obviously-fake values (`sk_test_x`, `whsec_test_secret`, `sk_live_xxx`) or the structural prefix table in `auth.LooksLikeAPIKey`. No real key material. |
| Hardcoded passwords (`password = "..."`) | **Clean.** Zero hits. |
| String-built SQL (`fmt.Sprintf` + SELECT/INSERT/UPDATE/DELETE) | **Clean.** Zero hits; all queries are sqlc-generated. The two store `Sprintf` hits format UUIDs/cursor tokens, not SQL. No raw `Exec/Query` outside sqlc. |
| `"math/rand"` | **Clean.** Zero hits. `internal/processor/retry.go` uses `math/rand/v2` for **retry-backoff jitter only** — documented inline as non-secret; all key/token/jti generation uses `crypto/rand`. |
| `==` on hashes/secrets | **Clean.** Only test assertions (non-emptiness/difference). Live comparisons use `hmac.Equal` (`auth.Validate`, `webhook.VerifySignature`); API keys and refresh tokens are matched by exact-hash DB lookup on HMAC output (standard, safe — an attacker cannot compute valid HMACs without the server secret); JWT verification is `golang-jwt/v5` (constant-time internally), with the signing method **pinned to HMAC** in `parseInto` (algorithm-confusion blocked). |

---

## Section B — Logging audit

### Finding B-1 (CRITICAL, fixed): every `log.Ctx(...)` call was a silent no-op
No logger was ever attached to any context and `zerolog.DefaultContextLogger` was unset, so zerolog returned its **disabled** logger for every `log.Ctx` call — API-key lookup failures, rate-limiter fail-open warnings, idempotency cache errors, the entire webhook-dispatcher lifecycle (including dead-letter warnings) were all discarded at runtime.

**Fix:** (1) `configureLogging` now sets `zerolog.DefaultContextLogger = &log.Logger` as a global safety net; (2) `main.go` attaches the logger to the root signal context handed to the dispatcher; (3) `requestLogger` in `internal/api/router.go` attaches a **request-scoped logger carrying `request_id`** to every request context, so all downstream handler/middleware log lines automatically include the request id.

### Finding B-2 (fixed): crucial events had no log trail
Added structured logs (level chosen per event class):

| Event | Level | Where |
|---|---|---|
| Unknown API key presented (hash prefix + IP, never the key) | WARN | `middleware/auth.go` |
| Revoked/expired API key used (key id, merchant, IP) | WARN | `middleware/auth.go` |
| Invalid JWT (signature/claims) | WARN (expired = DEBUG: routine) | `middleware/auth.go` |
| Insufficient scope (merchant, required vs held) | WARN | `middleware/auth.go` |
| Login failure (IP only — email is PII, never logged) / login success | WARN / INFO | `handlers/auth.go` |
| Refresh-token reuse/unknown rejected (possible theft) / rotation success | WARN / INFO | `handlers/auth.go` |
| Logout (jti revoked) | INFO | `handlers/auth.go` |
| **Every mutation** (action, resource, id, merchant, actor) — mirrors the audit row | INFO | `writeAudit` in `handlers/auth.go` |
| Charge created (amount, currency, processor, status, mode) / refunded (amounts, status) | INFO | `handlers/charges.go` |
| Payout created (amount, currency, processor, status) | INFO | `handlers/payouts.go` |
| Processor failures on charge/refund/payout/plan/subscription paths | ERROR | respective handlers |
| Processor transient retry (attempt, backoff) / retries exhausted | WARN / ERROR | `processor/retry.go` |
| Inbound webhook signature verification failure (source, IP, signature-present) — forgery signal | WARN | `handlers/webhooks.go` |
| Outbound delivery attempt failed → retry scheduled (attempt, status, next retry); dead-letter already logged | WARN | `webhook/dispatcher.go` |
| Rate-limit 429 (bucket, limit, client type, merchant) | WARN | `middleware/ratelimit.go` |
| Idempotent replay (merchant, key, status) / idempotency conflict (DB backstop hit) | INFO / WARN | `middleware/idempotency.go`, `handlers/charges.go` |
| Panics: stack + request_id (now via the request-scoped logger) | ERROR | `router.go` jsonRecoverer |

**Sensitive-data review of every log site (pre-existing + new):** no raw API keys, tokens, JWT strings, password material, Authorization headers, card/bank data, or request/response bodies are loggable from any call site. Identifiers used: record ids, key-hash prefix (8 hex chars of an HMAC — not reversible), display key prefix, jti, IPs, amounts/currencies (transaction facts, not credentials). Failed-login logs deliberately omit the attempted email (PII + enumeration aid).

### Finding B-3 (fixed): webhook-driven mutations bypassed the audit trail
`dispatchWebhookEvent` updated charge/payout/subscription status from inbound processor events with **no `audit_logs` row**. Now every applied event writes an audit entry with `actor_type=system`, `actor_id=webhook:<stripe|plaid>`, the resource table, record id, and merchant — so the trail shows the processor (not a person) acted. Cross-checked all other mutation paths: charges (create/refund), customers (create/update/delete), api keys (create/revoke), merchants (register), plans, subscriptions, bank accounts, payouts — all audited; audit `diff` fields carry no PII (ids and actions only).

### Finding B-4 (fixed): `writeAudit` silently discarded its error
`_ = h.audit.WriteAuditLog(...)` despite the comment claiming it logged. Audit-write failures now log at ERROR (never blocking the committed operation).

---

## Section C — Auth, secrets & crypto

| Check | Result |
|---|---|
| Secrets from env, fail-fast, ≥32 chars, distinct access/refresh, no usable defaults | **Pass.** `config.validate()` enforces length + distinctness; all secrets are `required` with no defaults. Defaults exist only for non-secrets (port, TTLs, limits, redis URL). |
| bcrypt cost ≥ 12 | **Pass.** `auth.PasswordCost = 12`, asserted by test. |
| JWT: HS256, separate secrets, claims | **Pass.** Signing method pinned to HMAC on parse; separate secrets enforced in both `config.validate` and `NewJWTManager`; claims carry `merchant_id`, `mode`, `scope`, `jti`. |
| Refresh tokens hashed at rest, atomic rotation, reuse rejected | **Pass.** Single-transaction `RotateRefreshToken`; reuse → 401 + WARN log (new). |
| API keys HMAC-hashed, plaintext shown once, constant-time validation | **Pass.** |
| **Revocation evicts the Redis auth cache** | **FINDING C-1 (CRITICAL, fixed).** `DeleteAPIKey` revoked in the DB but never evicted the cache — a revoked key kept authenticating for up to the 5-minute cache TTL. Added an `APIKeyCacheInvalidator` seam on `Handlers`; the `middleware.Authenticator` satisfies it and is now wired in `cmd/server/router.go`. Revoke → immediate `InvalidateAPIKeyCache`. Regression test added (`TestAPIKeyLifecycle` asserts exactly one eviction). |
| Scope enforcement on every route | **Pass (route-tree walk).** charges/plans/subscriptions → `RequireScope("write")`; bank-accounts/payouts → `RequireScope("admin")`; customers → write (key or JWT); apikeys/dashboard → JWT-only (dashboard JWTs carry admin); webhooks → signature-gated; refresh → the token itself is the credential. Group-level scopes match build.md exactly. |
| Mode isolation | **Pass.** Re-verified Phase 9 audit: every mode-bearing query filters by mode (get + list); adapters select credentials by mode; unit + integration tests cover cross-mode 404. |

---

## Section D — Input validation & money-safety

| Check | Result |
|---|---|
| `DisallowUnknownFields` + body cap on every request body | **Pass.** All request decoding goes through the single `bind()` helper (1 MiB cap, unknown fields rejected). No stray decoders. |
| No floats near money | **Pass.** Only two `float64` uses: `currency.ConvertMinorUnits` (documented display-only) and the Prometheus amount counter (metrics API requires float; it is telemetry, not money arithmetic). |
| Currency allowlist on every inbound currency | **FINDING D-1 (fixed).** `CreateBankAccount` accepted an unvalidated optional `currency` (an invalid value would have hit the `CHAR(3)` column as a 500). Now validated when present. |
| Currency case normalization | **FINDING D-2 (fixed).** Phase 8 made validation case-insensitive without normalizing persistence, so `"usd"` and `"USD"` could be stored as distinct values (skewing dashboard aggregates). Charges, plans, payouts, and bank accounts now uppercase the currency before validation/persistence. |
| UUID path params validated before queries | **Pass.** `store.textToUUID` rejects malformed ids; lookups map them to not-found sentinels → 404, never a query with attacker-shaped input. |
| Refund math, positive amounts, idempotency + DB UNIQUE backstop | **Pass.** Over-refund rejected (tested); amounts validated `> 0`; idempotency middleware on every authenticated POST; `UNIQUE(idempotency_key)` on charges/payouts with a 409 + WARN log (new) on conflict. |

---

## Section E — Infra, transport & web hardening

| Check | Result |
|---|---|
| Server timeouts | **Pass (pre-existing).** `ReadHeaderTimeout` 10s (slowloris), `ReadTimeout`/`WriteTimeout` 30s, `IdleTimeout` 120s. |
| Max request body | **Hardened.** Per-handler caps existed (bind / webhook reader, 1 MiB); added a router-global `chimw.RequestSize(1 MiB)` so every route rejects oversized payloads. |
| CORS | **Pass.** Exact-origin allowlist from `ALLOWED_ORIGINS`; credentials only ever paired with a specific echoed origin, never `*`; disallowed preflight → 403. |
| Security headers | **Added.** `X-Content-Type-Options: nosniff` + `X-Frame-Options: DENY` on every response. `/metrics` documented as proxy-restricted (route comment + README). HSTS belongs at the TLS-terminating proxy. |
| **Client-IP spoofing** | **FINDING E-1 (fixed; surfaced by staticcheck SA1019).** `chimw.RealIP` trusts the *leftmost* (client-controlled) `X-Forwarded-For` hop — letting callers spoof IPs past the per-IP login/register rate limit and into audit logs (GHSA-3fxj-6jh8-hvhx). Replaced with a `realIP` middleware that is **off by default** (TCP peer address) and, when `TRUST_PROXY_HEADERS=true` (new env var, documented), uses the **rightmost** XFF hop — the one appended by our own proxy. |
| DB TLS | **FINDING E-2 (fixed).** `sslmode=require` was documented but unenforced. `config.validate()` now refuses to start with `sslmode=disable` when `ENV=production`. Pool bounds were already set from env. |
| Inbound webhook fail-closed | **Pass.** Stripe: real signature + timestamp-tolerance verification (empty secret → reject all). Plaid: verifier rejects **everything** until JWT/JWKS verification is implemented; a nil verifier → 503; no bypass path exists. |
| Graceful shutdown order | **Pass.** Signal → root ctx cancels (dispatcher begins stopping) → server drains (30s deadline) → wait for dispatcher to finish recording in-flight outcomes → deferred Redis/pool close last. |

---

## Section F — Code quality gate

- `go vet ./...` (and `-tags=integration`): **zero issues**.
- `golangci-lint run` (v2, installed this pass): drove **6 → 0 issues**. The six: 3 unchecked `fmt.Fprintf` returns (hash/test writers — annotated `_, _ =`), the SA1019 RealIP vulnerability (real fix above), one tautological test comparison (rewritten), one unused test field (removed).
- Discarded errors: only two `_ =` remain — best-effort `godotenv.Load` and deferred `tx.Rollback` after commit (both standard); `writeAudit`'s discard was fixed (B-4).
- Goroutines: both long-lived goroutines (HTTP server, webhook dispatcher) shut down via context/`Shutdown` and are waited on.
- Doc comments: every hand-written exported symbol is documented (swept mechanically). **Justified exception:** sqlc-generated code in `internal/store/db/` has undocumented exported params structs — machine-generated, regenerated on schema change, conventionally exempt.

## Section G — Docs & final gates

- README: local + Docker setup, full env reference via `.env.example`, endpoint table, API-key/JWT auth guide, webhook signature-verification guide with constant-time code sample, test/live mode guide, PCI-DSS scope note — all present; added the `TRUST_PROXY_HEADERS` deployment note this pass.
- `.env.example` ⇄ `config.go`: **exact match** (mechanically diffed); the three extra vars are documented compose-only role passwords.
- Final gates: `go build` (the `make build` recipe) clean; `go test ./...` (the `make test` recipe) — **13/13 packages pass, zero failures**. *Caveat:* `make` itself and Docker are not installed on this audit host — the make targets were verified by running their exact underlying commands, and `docker compose up` / live `/health` remain validated-by-construction only (compose YAML parsed + checked; flagged in build.md Phase 13).

---

## Residual risk / deferred items (known and documented — none silent)

1. **Plaid inbound webhooks are non-functional by design** (fail-closed). Real JWT/JWKS verification must land before Plaid events can advance payment state. Until then, Plaid charge/payout status only changes via future polling or manual ops.
2. **Plaid transfer model gap** (Phase 6 note): ACH needs an `(access_token, account_id, user)` tuple the charge model doesn't capture yet — Plaid paths are wired but not end-to-end usable.
3. **`/webhook-endpoints` CRUD is not routed** — merchants cannot self-register outbound endpoints via API yet (dispatcher + signing are complete).
4. **`PUT /v1/subscriptions/{id}`** (plan/payment-method change) deferred from Phase 5.
5. **Docker/`make` unavailable on the audit host** — compose stack and containerized `/health` not executed here (validated by parsing + recipe-equivalent commands).
6. **Rate limiter fails open** on Redis outage (deliberate availability trade-off, logged at ERROR). Idempotency similarly degrades to the DB UNIQUE backstop for charges/payouts; non-charge POSTs would re-run. Acceptable for now; flagged for an SRE runbook.
7. **Metrics endpoint is unauthenticated in-process** — must be network-restricted at the proxy (documented).

## Recommendations before real money

1. Run the dockertest integration suite and `docker compose up` on a Docker-capable host (items the audit host could not execute).
2. Implement Plaid webhook JWKS verification and the Plaid transfer-model fields (residual items 1–2).
3. Add per-endpoint webhook-secret rotation and an ops runbook for the fail-open/degraded Redis modes.
4. External penetration test + a real PCI-DSS SAQ-A assessment — the in-code annotations are documentation aids, not an audit.
5. Wire log shipping + alerting on the new WARN/ERROR security events (they only have value if someone is paged on them).
