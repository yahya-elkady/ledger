# Payments Service

A production-shaped, Stripe-like **payments backend in Go**, backed by **PostgreSQL** and **Redis**. It exposes a REST API that wraps **Stripe** (cards, charges, subscriptions, payouts) and **Plaid** (ACH bank transfers) behind one consistent, multi-tenant, multi-currency interface — with API-key and JWT auth, idempotency, rate limiting, test/live isolation, webhooks, and PCI-DSS annotations throughout.

> **Status:** built in phases. Phases 1–9 are complete: scaffold, schema, auth, rate-limiting/idempotency, the full API handler layer, the real Stripe/Plaid processor adapters, the outbound webhook dispatcher, the multi-currency helpers, and verified test/live mode isolation (with a `make seed-test` fixture loader). Remaining: observability, the router/server assembly, and the integration test suite.

It sits on top of a small, self-contained **double-entry ledger** (`internal/ledger`, `internal/payment`) — the original core this project grew from — which models balanced money movement independently of any processor.

---

## What it does

Two kinds of clients talk to the same API:

- **Machine clients** (merchants integrating server-to-server) authenticate with **API keys** (`sk_live_…` / `pk_test_…`), scoped `read` / `write` / `admin`.
- **Dashboard users** (merchants managing their account) authenticate with **JWT** access + refresh tokens.

From there a merchant can manage customers, take card charges and ACH transfers, run subscriptions, pay out to bank accounts, receive processor webhooks, and read dashboard aggregates — all isolated per merchant and per **test/live mode**.

---

## Architecture at a glance

```
cmd/server            HTTP entrypoint: config, pools, graceful shutdown, /health
internal/
  config              env-based config; fails fast on missing secrets
  db                  pgxpool (bounded, ping-on-start)
  auth                pure crypto: API-key HMAC, JWT, bcrypt   (no I/O, unit-tested)
  api/
    respond           canonical JSON error envelope (leaf pkg)
    middleware         API-key + JWT auth, scope, mode, rate limit, idempotency
    handlers           REST handlers (depend on store *interfaces*, fake-tested)
  store               sqlc-backed persistence + domain mapping
    queries / db      hand-written SQL  ->  sqlc-generated type-safe Go
  models              response/domain structs (never carry secrets)
  ratelimit           Redis sliding-window limiter (atomic Lua)
  processor           processor seam: interfaces + Mux + retry/error mapping
    stripe / plaid    real vendor adapters
  webhook             inbound verifier seam + outbound dispatcher (signing, retries)
  ledger / payment    double-entry ledger core + payment state machine
migrations            numbered SQL, applied via psql (golang-migrate-ready)
```

**Design principle throughout:** handlers and the processor layer depend on small **interfaces**, not concrete types. The sqlc-backed stores, the Redis limiter, and the Stripe/Plaid adapters satisfy those interfaces in production; in tests, in-memory fakes do — so the entire request path is unit-testable without a database, Redis, or a network.

---

## Key features

- **Two auth models, one middleware stack.** API keys are stored only as HMAC-SHA256 hashes (plaintext shown once); JWTs use separate access/refresh secrets with atomic refresh-token rotation that rejects reuse. Scopes are hierarchical (`admin ⊇ write ⊇ read`).
- **Test/live mode isolation.** Every API key and JWT carries a mode; mode-bearing tables (`charges`, `subscriptions`, `payouts`, `plans`) filter by it, so test data and live data never mix. Processor adapters select the matching credentials per call — a live key is never used for a test request.
- **Idempotency.** `POST` writes require an `Idempotency-Key`; responses are cached in Redis (24h) and replayed verbatim, so a retried "create charge" never charges twice. The DB `UNIQUE(idempotency_key)` is the backstop.
- **Rate limiting.** A Redis sliding window implemented as a single atomic Lua script (`ZREMRANGEBYSCORE`+`ZADD`+`ZCARD`+`PEXPIRE`), keyed per identity, with `X-RateLimit-*` / `Retry-After` headers and fail-open behavior if Redis is down.
- **Processor abstraction with a router.** Handlers call one `processor.Processor`; a **`Mux`** routes charges/payouts to Stripe or Plaid per request, while subscriptions (Stripe-only) always go to Stripe. Every adapter call runs through a shared **retry loop** (exponential backoff + jitter on transient 5xx/429) and a normalized **error classifier**.
- **Outbound webhooks, signed and retried.** When a charge, payout, or subscription changes state, the service notifies the merchant's registered endpoints. Handlers only queue a pending delivery row (no merchant HTTP on the request path); a background dispatcher delivers with a 30s timeout and exponential-backoff retries (`60s · 2^attempt`), dead-lettering after the configured max attempts. Payloads are signed `HMAC-SHA256(secret, "<timestamp>.<payload>")` with a per-endpoint secret **derived** from the master signing secret — so no signing secret is ever stored — and the timestamp gives receivers 300-second replay protection.
- **Money is always integer minor units.** No floats in storage or processing. `internal/currency` holds the single ISO 4217 allowlist (`SupportedCurrencies`: USD, EUR, GBP, CAD, AUD, JPY, CHF, MXN, BRL, INR, SGD, HKD — each with name, symbol, minor-unit digits, and minimum charge) that every `amount`+`currency` pair is validated against. Zero-decimal currencies like JPY are handled without dividing by 100, and the service never performs server-side FX conversion — `ConvertMinorUnits`/`FormatAmount` are display-only helpers.
- **Append-only audit log.** Every mutation writes an `audit_logs` row (actor, action, resource, IP — never PII).
- **Security-first persistence.** Least-privilege `payments_app` DB role (no DDL), soft deletes on financial tables, parameterized sqlc queries only, and `// PCI-DSS:` annotations on every function that touches a processor.

---

## Request lifecycle (example: create a charge)

1. **Auth middleware** resolves the API key (Redis-cached) → injects merchant id, mode, scope.
2. **Scope + mode** middleware enforce `write` scope and a valid mode.
3. **Idempotency** middleware short-circuits if the key was seen before.
4. **Handler** validates the body (`int64` amount, ISO currency, target), then calls `processor.CreateCharge`.
5. The **`Mux`** routes to Stripe or Plaid; the adapter calls the SDK under the retry loop; a card decline comes back as a *recorded* `failed` charge, not an error.
6. The charge is **persisted** (mode-scoped), an **audit** row is written, and the canonical JSON response is returned.
7. A `charge.succeeded` (or `charge.failed`) **webhook event** is queued for the merchant's subscribed endpoints; the background dispatcher signs and delivers it with retries, off the request path.

---

## Tech stack

Go 1.26 · chi · pgx/v5 + sqlc · Redis (go-redis) · golang-jwt/v5 · bcrypt · stripe-go/v76 · plaid-go/v31 · zerolog · caarlos0/env + godotenv · miniredis (tests).

---

## Running it

> The HTTP server is assembled in a later phase; today the pieces are exercised by `go test` and a ledger smoke command. Local Postgres + Redis are expected.

```bash
cp .env.example .env          # fill in secrets (never commit .env)
make migrate-up               # apply migrations (or psql -f migrations/*.sql)
make test                     # full unit suite (uses in-memory fakes + miniredis)
make seed-test                # load test-mode fixtures (merchant, API key, customer, charges)
go run ./cmd/smoke            # double-entry ledger demo against Postgres
```

Configuration is entirely environment-driven (`internal/config`); the loader fails fast if a required secret is missing or too short, and never accepts hardcoded secrets. See [`.env.example`](.env.example) for the full variable reference.

### Database & migrations

Two schemas coexist in one database: the double-entry **ledger** (`accounts`, `entries`, `payments`) and the **payments-service** schema (`merchants`, `customers`, `charges`, `api_keys`, `subscriptions`, `payouts`, `webhooks`, `refresh_tokens`). Migrations are numbered SQL files applied with `psql` (or `make migrate-up` once golang-migrate is adopted); `sqlc generate` regenerates the type-safe query layer from `internal/store/queries`.

---

## Testing

Pure logic is unit-tested without external services: auth crypto, the rate-limiter (against miniredis), every middleware, all handlers (via store fakes), and the processor retry/error/routing logic. The Stripe and Plaid adapters compile against the real SDKs and have their non-network logic (mode-aware key selection, token parsing, amount formatting) tested directly; full sandbox integration tests are scheduled for the integration-test phase.

---

## License

Educational / learning-oriented build.
