// Command server is the payments-service HTTP entrypoint.
//
// Phase 1 scaffold: it loads configuration, opens the Postgres pool and the
// Redis client, wires a chi router with a health check, and runs an HTTP
// server with graceful shutdown. Auth, rate limiting, idempotency, and the
// business handlers are layered on in later phases.
//
// In production, TLS is terminated at the load balancer / reverse proxy; this
// server listens on plain HTTP internally (see README).
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/yahya-elkady/ledger/internal/config"
	"github.com/yahya-elkady/ledger/internal/db"
	"github.com/yahya-elkady/ledger/internal/store"
	"github.com/yahya-elkady/ledger/internal/webhook"
)

func main() {
	if err := run(); err != nil {
		log.Error().Err(err).Msg("server exited with error")
		os.Exit(1)
	}
}

// run wires every dependency and blocks until the server is shut down. It
// returns an error rather than calling os.Exit so deferred cleanup always runs.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	configureLogging(cfg)

	// Root context cancelled on SIGINT/SIGTERM — propagates to startup work and
	// long-lived clients. The global logger is attached so log.Ctx works in
	// background goroutines (dispatcher) that only receive this context.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx = log.Logger.WithContext(ctx)

	pool, err := db.NewPool(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer pool.Close()
	log.Info().Int32("max_conns", cfg.DatabaseMaxConns).Msg("database pool ready")

	redisOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("parsing REDIS_URL: %w", err)
	}
	rdb := redis.NewClient(redisOpts)
	defer func() { _ = rdb.Close() }()
	// Best-effort ping: Redis backs rate limiting and idempotency, which are
	// wired in later phases. A warning here keeps local dev unblocked.
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Warn().Err(err).Msg("redis ping failed at startup (rate limiting/idempotency degraded)")
	} else {
		log.Info().Msg("redis client ready")
	}

	// Outbound webhook dispatcher: delivers signed events to merchant endpoints
	// in the background (poll loop + exponential-backoff retries). It runs off
	// the request path and stops when the root context is cancelled.
	dispatcher := webhook.NewDispatcher(store.NewWebhookStore(pool), webhook.DispatcherConfig{
		SigningSecret: cfg.WebhookSigningSecret,
		MaxAttempts:   cfg.WebhookRetries,
		BaseBackoff:   cfg.WebhookRetryBackoff(),
	})
	dispatcherDone := make(chan struct{})
	go func() {
		defer close(dispatcherDone)
		dispatcher.Run(ctx)
	}()

	// The dispatcher doubles as the handlers' outbound event emitter: handlers
	// queue pending deliveries on the same store the poll loop drains.
	router := newRouter(cfg, pool, rdb, dispatcher)

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Run the server until the root context is cancelled by a signal.
	serverErr := make(chan error, 1)
	go func() {
		log.Info().Str("addr", srv.Addr).Str("env", cfg.Env).Msg("http server listening")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
		log.Info().Msg("shutdown signal received, draining connections")
	}

	// Graceful shutdown with a hard 30s deadline (build.md Phase 1).
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown failed: %w", err)
	}
	// The dispatcher stops via the root context; wait so in-flight webhook
	// attempts finish recording their outcome before the pool closes.
	select {
	case <-dispatcherDone:
	case <-shutdownCtx.Done():
		log.Warn().Msg("webhook dispatcher did not stop before shutdown deadline")
	}
	log.Info().Msg("server stopped cleanly")
	return nil
}

// configureLogging sets up zerolog: human-friendly console output in
// development, structured JSON in production, at the configured level.
func configureLogging(cfg *config.Config) {
	level, err := zerolog.ParseLevel(cfg.LogLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)
	zerolog.TimeFieldFormat = time.RFC3339

	if !cfg.IsProduction() {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})
	}
	// Safety net: log.Ctx(ctx) on a context with no attached logger falls back
	// to the global logger instead of zerolog's disabled logger. Without this,
	// every log.Ctx call on an unadorned context is silently discarded.
	zerolog.DefaultContextLogger = &log.Logger
}
