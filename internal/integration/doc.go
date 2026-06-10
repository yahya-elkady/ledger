// Package integration holds the dockertest-backed end-to-end tests that exercise
// the full HTTP stack (router → middleware → handlers → stores) against a real
// PostgreSQL and Redis spun up in throwaway containers.
//
// The tests are guarded by the `integration` build tag, so the default
// `go test ./...` / `make test` (unit suite) neither compiles nor runs them and
// stays fast and Docker-free. Run them with `make test-integration`
// (`go test -tags=integration ./internal/integration/...`); they skip cleanly
// when no Docker daemon is reachable.
//
// Processor calls (Stripe/Plaid) are faked — these tests cover the service's own
// HTTP/persistence/middleware behavior, not vendor APIs — while the database,
// migrations, Redis, auth, rate limiting, and idempotency are all real.
package integration
