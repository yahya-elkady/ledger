package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yahya-elkady/ledger/internal/api"
	"github.com/yahya-elkady/ledger/internal/api/handlers"
	"github.com/yahya-elkady/ledger/internal/api/middleware"
	"github.com/yahya-elkady/ledger/internal/auth"
	"github.com/yahya-elkady/ledger/internal/config"
	"github.com/yahya-elkady/ledger/internal/processor"
	"github.com/yahya-elkady/ledger/internal/processor/plaid"
	"github.com/yahya-elkady/ledger/internal/processor/stripe"
	"github.com/yahya-elkady/ledger/internal/ratelimit"
	"github.com/yahya-elkady/ledger/internal/store"
	"github.com/yahya-elkady/ledger/internal/webhook"
)

// authRatePerMin caps unauthenticated auth attempts (register/login) per IP.
const authRatePerMin = 10

// newRouter wires every dependency — stores, auth crypto, processor adapters,
// webhook verifiers, middleware — and builds the full /v1 route tree via the
// api package. The dispatcher (passed in) is reused as the outbound event
// emitter so handlers queue webhook deliveries on the same store.
func newRouter(cfg *config.Config, pool *pgxpool.Pool, rdb *redis.Client, emitter handlers.EventEmitter) http.Handler {
	// Auth crypto.
	jwtMgr, err := auth.NewJWTManager(cfg.JWTAccessSecret, cfg.JWTRefreshSecret, cfg.AccessTTL(), cfg.RefreshTTL())
	if err != nil {
		// Config.validate already guarantees distinct, long-enough secrets, so a
		// failure here is a programming error.
		panic(err)
	}
	hasher := auth.NewAPIKeyHasher(cfg.APIKeyHMACSecret)

	// Processor adapters behind the routing Mux (Stripe owns subscriptions; Plaid
	// handles ACH charges/payouts).
	stripeClient := stripe.New(stripe.Config{
		LiveKey: cfg.StripeSecretKeyLive,
		TestKey: cfg.StripeSecretKeyTest,
	})
	plaidClient := plaid.New(plaid.Config{
		ClientID:         cfg.PlaidClientID,
		SandboxSecret:    cfg.PlaidSecretSandbox,
		ProductionSecret: cfg.PlaidSecretLive,
	})
	proc := processor.NewMux(stripeClient, map[string]processor.ChargePayoutProcessor{
		processor.Plaid: plaidClient,
	})

	// Stores. AuthStore backs both the API-key and refresh-token handler seams.
	authStore := store.NewAuthStore(pool)
	billingStore := store.NewBillingStore(pool)
	payoutStore := store.NewPayoutStore(pool)

	h := handlers.New(handlers.Deps{
		Merchants:     store.NewMerchantStore(pool),
		APIKeys:       authStore,
		Tokens:        authStore,
		Customers:     store.NewCustomerStore(pool),
		Charges:       store.NewChargeStore(pool),
		Plans:         billingStore,
		Subscriptions: billingStore,
		BankAccounts:  payoutStore,
		Payouts:       payoutStore,
		Dashboard:     store.NewDashboardStore(pool),
		Audit:         store.NewAuditStore(pool),
		Events:        emitter,
		Processor:     proc,
		StripeWebhook: webhook.NewStripeVerifier(cfg.StripeWebhookSecret),
		PlaidWebhook:  webhook.NewPlaidVerifier(),
		JWT:           jwtMgr,
		Hasher:        hasher,
		AccessTTL:     cfg.AccessTTL(),
	})

	// Middleware.
	authn := middleware.NewAuthenticator(authStore, jwtMgr, hasher, rdb)
	limiter := ratelimit.NewRateLimiter(rdb)
	rl := middleware.NewRateLimitMiddleware(limiter, cfg.RateLimitLiveRPM, cfg.RateLimitTestRPM, cfg.RateLimitDashboardRPM)
	idem := middleware.NewIdempotency(rdb)

	return api.NewRouter(api.RouterDeps{
		Handlers:       h,
		Auth:           authn,
		RateLimit:      rl,
		Idempotency:    idem,
		AllowedOrigins: cfg.AllowedOrigins,
		Health:         healthHandler(pool, rdb),
		AuthRatePerMin: authRatePerMin,
	})
}

// healthResponse is the body returned by GET /health.
type healthResponse struct {
	Status string `json:"status"`
	DB     string `json:"db"`
	Redis  string `json:"redis"`
}

// healthHandler reports liveness of the process and its backing stores. It
// returns 200 when both Postgres and Redis answer a ping, 503 otherwise.
func healthHandler(pool *pgxpool.Pool, rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()

		resp := healthResponse{Status: "ok", DB: "ok", Redis: "ok"}
		code := http.StatusOK

		if err := pool.Ping(ctx); err != nil {
			resp.DB = "down"
			resp.Status = "degraded"
			code = http.StatusServiceUnavailable
		}
		if err := rdb.Ping(ctx).Err(); err != nil {
			resp.Redis = "down"
			resp.Status = "degraded"
			code = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(resp)
	}
}
