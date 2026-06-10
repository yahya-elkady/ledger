// Package api assembles the HTTP route tree and the global middleware stack that
// front every handler. It wires the auth, rate-limit, mode, and idempotency
// middleware (built in earlier phases) onto the chi router in the order the
// build spec mandates, and supplies consistent JSON for 404/405/panic and a
// CORS preflight gate driven by the configured origin allowlist.
package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog/log"

	"github.com/yahya-elkady/ledger/internal/api/handlers"
	"github.com/yahya-elkady/ledger/internal/api/middleware"
	"github.com/yahya-elkady/ledger/internal/api/respond"
	"github.com/yahya-elkady/ledger/internal/auth"
)

// RouterDeps are the dependencies needed to build the router. The middleware and
// handlers are constructed by the caller (cmd/server) from concrete stores;
// Health is the liveness handler (it pings Postgres + Redis).
type RouterDeps struct {
	Handlers       *handlers.Handlers
	Auth           *middleware.Authenticator
	RateLimit      *middleware.RateLimitMiddleware
	Idempotency    *middleware.Idempotency
	AllowedOrigins []string
	Health         http.HandlerFunc
	// AuthRatePerMin caps unauthenticated auth attempts (register/login) per IP.
	AuthRatePerMin int
}

// NewRouter builds the full /v1 route tree with the ordered middleware stack.
func NewRouter(d RouterDeps) http.Handler {
	h, a, rl, idem := d.Handlers, d.Auth, d.RateLimit, d.Idempotency

	r := chi.NewRouter()

	// Global stack (build.md middleware order): request id, real ip, structured
	// request log, panic recovery (-> JSON 500), then CORS preflight handling.
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(requestLogger)
	r.Use(jsonRecoverer)
	r.Use(corsMiddleware(d.AllowedOrigins))

	// Consistent JSON for unmatched routes / methods.
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		respond.Error(w, req, http.StatusNotFound, respond.CodeNotFound, "the requested resource does not exist")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		respond.Error(w, req, http.StatusMethodNotAllowed, respond.CodeMethodNotAllowed, "method not allowed for this resource")
	})

	// Liveness — no auth.
	r.Get("/health", d.Health)

	r.Route("/v1", func(r chi.Router) {
		// --- auth: unauthenticated (per-IP limited) + logout (JWT) -----------
		r.Route("/auth", func(r chi.Router) {
			r.With(rl.PerIP(d.AuthRatePerMin)).Post("/register", h.Register)
			r.With(rl.PerIP(d.AuthRatePerMin)).Post("/login", h.Login)
			r.Post("/refresh", h.Refresh) // no auth; rotates a presented refresh token
			r.With(a.JWTMiddleware).Post("/logout", h.Logout)
		})

		// --- API keys: dashboard (JWT) only ----------------------------------
		r.Route("/apikeys", func(r chi.Router) {
			r.Use(a.JWTMiddleware)
			r.Use(middleware.ModeMiddleware)
			r.Use(rl.Handler)
			r.With(idem.Handler).Post("/", h.CreateAPIKey)
			r.Get("/", h.ListAPIKeys)
			r.Get("/{id}", h.GetAPIKey)
			r.Delete("/{id}", h.DeleteAPIKey)
		})

		// --- customers: API key (write) OR dashboard JWT ---------------------
		r.Route("/customers", func(r chi.Router) {
			r.Use(eitherAuth(a))
			r.Use(middleware.ModeMiddleware)
			r.Use(rl.Handler)
			r.Use(middleware.RequireScope("write"))
			r.With(idem.Handler).Post("/", h.CreateCustomer)
			r.Get("/", h.ListCustomers)
			r.Get("/{id}", h.GetCustomer)
			r.Put("/{id}", h.UpdateCustomer)
			r.Delete("/{id}", h.DeleteCustomer)
		})

		// --- charges: API key write ------------------------------------------
		r.Route("/charges", func(r chi.Router) {
			apiKeyScoped(r, a, rl, "write")
			r.With(idem.Handler).Post("/", h.CreateCharge)
			r.Get("/", h.ListCharges)
			r.Get("/{id}", h.GetCharge)
			r.With(idem.Handler).Post("/{id}/refund", h.RefundCharge)
		})

		// --- plans: API key write --------------------------------------------
		r.Route("/plans", func(r chi.Router) {
			apiKeyScoped(r, a, rl, "write")
			r.With(idem.Handler).Post("/", h.CreatePlan)
			r.Get("/", h.ListPlans)
			r.Delete("/{id}", h.DeletePlan)
		})

		// --- subscriptions: API key write ------------------------------------
		r.Route("/subscriptions", func(r chi.Router) {
			apiKeyScoped(r, a, rl, "write")
			r.With(idem.Handler).Post("/", h.CreateSubscription)
			r.Get("/", h.ListSubscriptions)
			r.Get("/{id}", h.GetSubscription)
			r.With(idem.Handler).Post("/{id}/cancel", h.CancelSubscription)
			// PUT /{id} (plan/payment-method change) is deferred — no handler yet.
		})

		// --- bank accounts: API key admin ------------------------------------
		r.Route("/bank-accounts", func(r chi.Router) {
			apiKeyScoped(r, a, rl, "admin")
			r.With(idem.Handler).Post("/", h.CreateBankAccount)
			r.Get("/", h.ListBankAccounts)
			r.Delete("/{id}", h.DeleteBankAccount)
		})

		// --- payouts: API key admin ------------------------------------------
		r.Route("/payouts", func(r chi.Router) {
			apiKeyScoped(r, a, rl, "admin")
			r.With(idem.Handler).Post("/", h.CreatePayout)
			r.Get("/", h.ListPayouts)
			r.Get("/{id}", h.GetPayout)
		})

		// --- inbound webhooks: no auth, verified by signature ----------------
		// No identity rate limit: processors legitimately burst and retry.
		r.Route("/webhooks", func(r chi.Router) {
			r.Post("/stripe", h.StripeWebhook)
			r.Post("/plaid", h.PlaidWebhook)
		})

		// --- dashboard: JWT only ---------------------------------------------
		r.Route("/dashboard", func(r chi.Router) {
			r.Use(a.JWTMiddleware)
			r.Use(middleware.ModeMiddleware)
			r.Use(rl.Handler)
			r.Get("/overview", h.DashboardOverview)
			r.Get("/transactions", h.DashboardTransactions)
		})

		// NOTE: /webhook-endpoints CRUD (register endpoints, return the derived
		// secret once) is deferred — its handlers + store are a follow-up; the
		// outbound dispatcher (Phase 7) is otherwise complete.
	})

	return r
}

// apiKeyScoped applies the standard API-key middleware chain to a route group:
// API-key auth, mode enforcement, per-identity rate limiting, and a minimum
// scope. POST routes additionally add idempotency at the route via r.With.
func apiKeyScoped(r chi.Router, a *middleware.Authenticator, rl *middleware.RateLimitMiddleware, scope string) {
	r.Use(a.APIKeyMiddleware)
	r.Use(middleware.ModeMiddleware)
	r.Use(rl.Handler)
	r.Use(middleware.RequireScope(scope))
}

// eitherAuth dispatches to API-key auth when the bearer token looks like an API
// key, otherwise to JWT auth — so a route can accept either credential. Both
// downstream middlewares inject merchant/mode/scope, so RequireScope and the
// handlers work identically afterwards (dashboard JWTs carry admin scope).
func eitherAuth(a *middleware.Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		apiKeyChain := a.APIKeyMiddleware(next)
		jwtChain := a.JWTMiddleware(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tok, ok := bearerToken(r); ok && auth.LooksLikeAPIKey(tok) {
				apiKeyChain.ServeHTTP(w, r)
				return
			}
			jwtChain.ServeHTTP(w, r)
		})
	}
}

// bearerToken extracts the token from an `Authorization: Bearer <token>` header.
func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	tok := strings.TrimSpace(h[len(prefix):])
	return tok, tok != ""
}

// requestLogger logs one structured line per request: method, path, status,
// latency, request id. It never logs the body or Authorization header.
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		next.ServeHTTP(ww, r)
		log.Info().
			Str("request_id", chimw.GetReqID(r.Context())).
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Int("status", ww.Status()).
			Dur("latency", time.Now().Sub(start)).
			Msg("http request")
	})
}

// jsonRecoverer recovers from a handler panic, logs it with the request id, and
// returns the canonical JSON 500 envelope (rather than chi's plaintext default).
func jsonRecoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error().
					Str("request_id", chimw.GetReqID(r.Context())).
					Interface("panic", rec).
					Msg("recovered from panic")
				respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// corsMiddleware answers OPTIONS preflight and sets CORS headers, allowing only
// origins in the configured allowlist. With no configured origins it is a no-op
// passthrough (CORS effectively disabled — same-origin/server-to-server only).
func corsMiddleware(allowed []string) func(http.Handler) http.Handler {
	set := make(map[string]bool, len(allowed))
	for _, o := range allowed {
		if o = strings.TrimSpace(o); o != "" {
			set[o] = true
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && set[origin] {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key")
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
			if r.Method == http.MethodOptions {
				// Preflight: allowed origin -> 204; disallowed -> 403.
				if origin != "" && set[origin] {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				w.WriteHeader(http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
