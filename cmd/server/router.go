package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/redis/go-redis/v9"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yahya-elkady/ledger/internal/config"
)

// newRouter builds the Phase 1 router: baseline middleware plus a health check.
// The full /v1 route tree (auth, charges, subscriptions, …) is assembled in a
// later phase; this is the minimal wiring needed to stand the server up.
func newRouter(cfg *config.Config, pool *pgxpool.Pool, rdb *redis.Client) http.Handler {
	r := chi.NewRouter()

	// Baseline middleware. The full ordered stack (rate limit, auth, mode,
	// idempotency) is added with the route tree in a later phase.
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	r.Get("/health", healthHandler(pool, rdb))

	return r
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
