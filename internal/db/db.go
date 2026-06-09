// Package db owns the application's PostgreSQL connection pool.
//
// Security notes (see build.md "Database Security Rules"):
//   - The app connects with a least-privilege role (payments_app), never the
//     postgres superuser. The role is provisioned by the docker init script.
//   - Connections are pooled and bounded by DATABASE_MAX_CONNS / MIN_CONNS so
//     the app never opens unbounded connections.
//   - sslmode=require in all environments; sslmode=disable is permitted only in
//     development, and the connection string documents that explicitly.
//   - Encryption at rest is handled at the infrastructure layer (encrypted
//     PostgreSQL volume/disk) — out of scope for application code.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yahya-elkady/ledger/internal/config"
)

// NewPool builds a bounded pgx connection pool from configuration and verifies
// connectivity with a ping. It fails fast: if the database is unreachable, the
// process should not start.
func NewPool(ctx context.Context, cfg *config.Config) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing DATABASE_URL: %w", err)
	}

	poolCfg.MaxConns = cfg.DatabaseMaxConns
	poolCfg.MinConns = cfg.DatabaseMinConns
	poolCfg.MaxConnLifetime = time.Hour
	poolCfg.MaxConnIdleTime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("opening connection pool: %w", err)
	}

	// Ping so a wrong password or unreachable host surfaces now, not on the
	// first query under load.
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	return pool, nil
}
