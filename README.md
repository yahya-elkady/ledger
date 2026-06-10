# Payments Service

A production-shaped, Stripe-like **payments backend in Go**, backed by **PostgreSQL** and **Redis**. It exposes a REST API that wraps **Stripe** (cards, charges, subscriptions, payouts) and **Plaid** (ACH bank transfers) behind one consistent, multi-tenant, multi-currency interface — with API-key and JWT auth, idempotency, rate limiting, test/live isolation, webhooks, and PCI-DSS annotations throughout.

> **Status:** all 14 build phases complete — scaffold, schema, auth, rate-limiting/idempotency, the full API handler layer, the real Stripe/Plaid processor adapters, the outbound webhook dispatcher, multi-currency helpers, verified test/live mode isolation, the assembled chi router, observability, the test suite (unit + dockertest end-to-end), Docker/Compose deployment, and a **final security review & hardening pass** (findings, fixes, and residual risks documented in [`SECURITY_AUDIT.md`](SECURITY_AUDIT.md)).

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
    router            full /v1 route tree + ordered middleware stack, CORS, 404/405
    respond           canonical JSON error envelope (leaf pkg)
    middleware         API-key + JWT auth, scope, mode, rate limit, idempotency
    handlers           REST handlers (depend on store *interfaces*, fake-tested)
  store               sqlc-backed persistence + domain mapping
    queries / db      hand-written SQL  ->  sqlc-generated type-safe Go
  models              response/domain structs (never carry secrets)
  ratelimit           Redis sliding-window limiter (atomic Lua)
  metrics             Prometheus collectors + record helpers (leaf pkg)
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
- **Observability built in.** Every request logs one structured zerolog line (`request_id`, method, path, status, `latency_ms`, and `merchant_id`/`mode` once authed — never the body or auth headers), and Prometheus metrics are exposed at `/metrics` (request counts/latency, charge counts/volume, rate-limit hits, webhook delivery outcomes). The HTTP `path` label is the route pattern, not the raw URL, so per-id paths don't blow up cardinality. Panics are recovered, logged with a full stack trace and the request id, and returned as the canonical JSON 500.
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

### With Docker Compose (recommended)

The whole stack — API, PostgreSQL, Redis — comes up with one command. Postgres provisions the least-privilege roles on first init, a one-shot `migrate` job applies the schema, and the API starts only once migrations finish.

```bash
cp .env.example .env           # fill in the JWT/HMAC/webhook secrets (>= 32 chars each)
docker compose up --build      # starts postgres + redis + migrate + api
curl localhost:8080/health     # -> {"status":"ok","db":"ok","redis":"ok"}
docker compose down -v         # stop and wipe volumes
```

The `api` image is a multi-stage, static, **non-root distroless** build. Migrations run in a dedicated `migrate` service as the `payments_migrations` role (the app's `payments_app` role has no DDL rights by design); applied files are tracked in a `schema_migrations` table so re-runs are idempotent.

### Without Docker (local toolchain)

Local Postgres + Redis expected; configure `DATABASE_URL`/`REDIS_URL` in `.env`.

```bash
cp .env.example .env          # fill in secrets (never commit .env)
make migrate-up               # apply migrations (or psql -f migrations/*.sql)
make test                     # unit suite (in-memory fakes + miniredis)
make seed-test                # load test-mode fixtures (merchant, API key, customer, charges)
make run                      # start the HTTP server (GET /health to check)
go run ./cmd/smoke            # double-entry ledger demo against Postgres
```

Configuration is entirely environment-driven (`internal/config`); the loader fails fast if a required secret is missing or too short (and refuses `sslmode=disable` in production), and never accepts hardcoded secrets. See [`.env.example`](.env.example) for the full variable reference.

> **Behind a load balancer:** set `TRUST_PROXY_HEADERS=true` so per-IP rate limits and audit logs use the real client IP from `X-Forwarded-For` (rightmost hop). Leave it `false` (the default) when clients connect directly — otherwise they can spoof their IP.

### Database & migrations

Two schemas coexist in one database: the double-entry **ledger** (`accounts`, `entries`, `payments`) and the **payments-service** schema (`merchants`, `customers`, `charges`, `api_keys`, `subscriptions`, `payouts`, `webhooks`, `refresh_tokens`). Migrations are numbered SQL files applied with `psql` (or `make migrate-up` once golang-migrate is adopted); `sqlc generate` regenerates the type-safe query layer from `internal/store/queries`.

---

## Testing

Pure logic is unit-tested without external services: auth crypto, the rate-limiter (against miniredis), every middleware, all handlers (via store fakes), the router wiring, the currency and metrics helpers, the Stripe webhook verifier, and the processor retry/error/routing logic. `make test` runs this suite — fast and Docker-free.

A **dockertest end-to-end suite** (`internal/integration`, behind a `//go:build integration` tag) exercises the real router → middleware → stores path against throwaway `postgres:16` + `redis:7` containers: register/login, API-key generation and use, the full charge flow with DB assertions, idempotent replay, rate-limit 429s, and test/live mode isolation. Run it with `make test-integration` (requires a Docker daemon; only the Stripe/Plaid processor is faked). The Stripe/Plaid adapters' own non-network logic (mode-aware key selection, token parsing, amount formatting) is unit-tested directly.

---

## API reference

Base path `/v1`. Errors use one envelope: `{"error","message","param?","request_id"}`. Collections return `{"data":[...],"next_cursor":"..."}`. All `POST` writes behind auth require an `Idempotency-Key` header. Amounts are integer minor units; currencies are validated against the ISO 4217 allowlist.

| Method & path | Auth | Notes |
|---|---|---|
| `POST /v1/auth/register` | none | create merchant, returns access + refresh tokens (10/min per IP) |
| `POST /v1/auth/login` | none | returns tokens; generic 401 on bad creds (10/min per IP) |
| `POST /v1/auth/refresh` | none | rotates the refresh token (reuse rejected) |
| `POST /v1/auth/logout` | JWT | revokes the refresh token |
| `POST/GET /v1/apikeys`, `GET/DELETE /v1/apikeys/{id}` | JWT | key plaintext shown once on create |
| `POST/GET /v1/customers`, `GET/PUT/DELETE /v1/customers/{id}` | API key (write) or JWT | |
| `POST/GET /v1/charges`, `GET /v1/charges/{id}`, `POST /v1/charges/{id}/refund` | API key (write) | |
| `POST/GET /v1/plans`, `DELETE /v1/plans/{id}` | API key (write) | |
| `POST/GET /v1/subscriptions`, `GET /v1/subscriptions/{id}`, `POST /v1/subscriptions/{id}/cancel` | API key (write) | |
| `POST/GET /v1/bank-accounts`, `DELETE /v1/bank-accounts/{id}` | API key (admin) | |
| `POST/GET /v1/payouts`, `GET /v1/payouts/{id}` | API key (admin) | |
| `POST /v1/webhooks/stripe`, `POST /v1/webhooks/plaid` | signature | inbound processor events |
| `GET /v1/dashboard/overview`, `GET /v1/dashboard/transactions` | JWT | mode-isolated aggregates |
| `GET /health`, `GET /metrics` | none | liveness; Prometheus scrape (restrict `/metrics` at the proxy in prod) |

### Authentication

- **Machine clients** send `Authorization: Bearer sk_live_…` / `sk_test_…`. Keys are scoped `read` < `write` < `admin`; only the HMAC hash is stored.
- **Dashboard users** send `Authorization: Bearer <jwt>`. Access tokens last 15 min; refresh with `POST /v1/auth/refresh` (rotation invalidates the old token).

## Webhook signature verification (for merchants)

Outbound events POST a JSON body `{"id","type","created","data"}` with two headers:

- `X-Payments-Timestamp: <unix seconds>`
- `X-Payments-Signature: sha256=<hex hmac>`

The signature is `HMAC-SHA256(endpoint_secret, "<timestamp>.<raw_body>")`. To verify, recompute it over the **raw** request body and compare in constant time, then reject timestamps outside a tolerance window (we use 300s) to block replays:

```python
import hashlib, hmac, time
def verify(raw_body: bytes, ts: str, sig: str, secret: str) -> bool:
    if abs(time.time() - int(ts)) > 300:          # replay window
        return False
    mac = hmac.new(secret.encode(), f"{ts}.".encode() + raw_body, hashlib.sha256)
    expected = "sha256=" + mac.hexdigest()
    return hmac.compare_digest(expected, sig)      # constant-time
```

The per-endpoint secret is shown once when the endpoint is registered; it is derived from the service's master signing secret and never stored in plaintext.

## Test vs live mode

Every API key and JWT carries a `mode` (`test` or `live`); mode-bearing tables (`charges`, `subscriptions`, `payouts`, `plans`) filter by it, so test and live data never mix. A test-mode key cannot read a live-mode charge (returns 404) and vice versa. The processor adapters select credentials by mode — a live Stripe/Plaid key is never used for a test request, and sandbox vs production environments are chosen accordingly. Use `make seed-test` to load test-mode fixtures. Test mode also has lower rate limits (`RATE_LIMIT_TEST_RPM`).

## PCI-DSS notes

This service is built to **avoid handling raw cardholder data** — it stores only processor tokens/ids (`pm_…`, `ch_…`), never PANs, CVVs, or full bank numbers. Card capture should use the processor's client-side tokenization (e.g. Stripe Elements) so raw card data never reaches this server, keeping it in PCI-DSS SAQ-A scope. Supporting controls in this codebase: `// PCI-DSS:` annotations on every function that touches payment instruments or processor calls; structured logs that never include card numbers, CVVs, account numbers, or raw API keys (IDs and masked values only); API keys, refresh tokens, and webhook secrets stored only as hashes/derived secrets; TLS terminated at the proxy with the app on internal HTTP; and an append-only `audit_logs` trail for every mutation. These annotations are documentation aids, not a substitute for a formal audit.

---

## License

Educational / learning-oriented build.
