package store

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

// Pool is a connection pool to the Postgres database.
// A pool manages multiple connections automatically — rather than opening
// and closing one connection per query, it keeps a set open and reuses them.
// This is what you want for a server that handles many requests.
type Pool = pgxpool.Pool

// Connect opens a connection pool using the DATABASE_URL environment variable.
// Call this once at startup, hold onto the pool for the lifetime of the app,
// and call Close() when the app shuts down.
//
// Expected format:
//
//	DATABASE_URL=postgres://user:password@localhost:5432/ledger
func Connect(ctx context.Context) (*pgxpool.Pool, error) {
	// Load .env for local development before reading the environment.
	// A missing .env is not an error — in production the variables are
	// provided by the environment directly, so we ignore the load error.
	_ = godotenv.Load()

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return nil, fmt.Errorf("DATABASE_URL environment variable is not set")
	}

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("opening connection pool: %w", err)
	}

	// Ping the database to confirm the connection is actually working.
	// Without this, a wrong password or unreachable host won't surface
	// until the first query fires.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	return pool, nil
}