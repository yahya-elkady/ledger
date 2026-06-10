// Package config loads all runtime configuration from environment variables.
//
// Configuration is the single place secrets enter the process. Per the build's
// security rules, NO secret is ever hardcoded — every value comes from the
// environment (loaded from a local .env in development via godotenv, and from
// the real environment in production). Load fails fast with a clear error if a
// required variable is missing, so the process never starts half-configured.
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

// Config is the fully-parsed application configuration. Fields map to the
// variables documented in .env.example. Required fields carry `env:"...,required"`
// and cause Load to fail if unset; optional integrations (Stripe, Plaid) are
// left unrequired so the service can boot in development without them.
type Config struct {
	// Server
	Port     int    `env:"PORT" envDefault:"8080"`
	Env      string `env:"ENV" envDefault:"development"`
	LogLevel string `env:"LOG_LEVEL" envDefault:"info"`

	// Database — never use a superuser in app; use a least-privilege role.
	DatabaseURL      string `env:"DATABASE_URL,required"`
	DatabaseMaxConns int32  `env:"DATABASE_MAX_CONNS" envDefault:"25"`
	DatabaseMinConns int32  `env:"DATABASE_MIN_CONNS" envDefault:"5"`

	// JWT — separate secrets for access vs refresh tokens. TTLs are expressed in
	// their natural units (minutes/days) per .env.example; use the TTL accessors
	// below to get time.Duration values.
	JWTAccessSecret     string `env:"JWT_ACCESS_SECRET,required"`
	JWTRefreshSecret    string `env:"JWT_REFRESH_SECRET,required"`
	JWTAccessTTLMinutes int    `env:"JWT_ACCESS_TTL_MINUTES" envDefault:"15"`
	JWTRefreshTTLDays   int    `env:"JWT_REFRESH_TTL_DAYS" envDefault:"30"`

	// API keys — HMAC-SHA256 secret used to hash keys at rest.
	APIKeyHMACSecret string `env:"API_KEY_HMAC_SECRET,required"`

	// Redis — rate limiting + idempotency cache.
	RedisURL string `env:"REDIS_URL" envDefault:"redis://localhost:6379"`

	// Stripe — optional in development.
	StripeSecretKeyLive string `env:"STRIPE_SECRET_KEY_LIVE"`
	StripeSecretKeyTest string `env:"STRIPE_SECRET_KEY_TEST"`
	StripeWebhookSecret string `env:"STRIPE_WEBHOOK_SECRET"`

	// Plaid — optional in development.
	PlaidClientID      string `env:"PLAID_CLIENT_ID"`
	PlaidSecretLive    string `env:"PLAID_SECRET_LIVE"`
	PlaidSecretSandbox string `env:"PLAID_SECRET_SANDBOX"`
	PlaidEnv           string `env:"PLAID_ENV" envDefault:"sandbox"`

	// Outbound webhook delivery.
	WebhookSigningSecret   string `env:"WEBHOOK_SIGNING_SECRET,required"`
	WebhookRetries         int    `env:"WEBHOOK_DELIVERY_RETRIES" envDefault:"5"`
	WebhookRetryBackoffSec int    `env:"WEBHOOK_RETRY_BACKOFF_SECONDS" envDefault:"60"`

	// Rate limits (requests per minute).
	RateLimitLiveRPM      int `env:"RATE_LIMIT_LIVE_RPM" envDefault:"1000"`
	RateLimitTestRPM      int `env:"RATE_LIMIT_TEST_RPM" envDefault:"100"`
	RateLimitDashboardRPM int `env:"RATE_LIMIT_DASHBOARD_RPM" envDefault:"300"`

	// CORS — comma-separated allowlist of origins for the dashboard API.
	AllowedOrigins []string `env:"ALLOWED_ORIGINS" envSeparator:","`

	// TrustProxyHeaders derives client IPs from X-Forwarded-For. Enable ONLY
	// behind a proxy/load balancer that always appends the real client IP;
	// otherwise clients can spoof their IP (rate-limit evasion, bad audit IPs).
	TrustProxyHeaders bool `env:"TRUST_PROXY_HEADERS" envDefault:"false"`
}

// Load reads .env (best-effort, for local development) and then parses the
// environment into a Config. A missing .env is not an error — production
// supplies variables directly. A missing *required* variable is a fatal error.
func Load() (*Config, error) {
	// Best-effort: in production the variables come from the real environment,
	// so a missing .env file is fine and intentionally ignored.
	_ = godotenv.Load()

	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("parsing environment configuration: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// IsProduction reports whether the service is running in production mode.
func (c *Config) IsProduction() bool {
	return c.Env == "production"
}

// AccessTTL returns the access-token lifetime as a Duration.
func (c *Config) AccessTTL() time.Duration {
	return time.Duration(c.JWTAccessTTLMinutes) * time.Minute
}

// RefreshTTL returns the refresh-token lifetime as a Duration.
func (c *Config) RefreshTTL() time.Duration {
	return time.Duration(c.JWTRefreshTTLDays) * 24 * time.Hour
}

// WebhookRetryBackoff returns the base webhook-retry backoff as a Duration.
func (c *Config) WebhookRetryBackoff() time.Duration {
	return time.Duration(c.WebhookRetryBackoffSec) * time.Second
}

// validate enforces cross-field invariants that struct tags cannot express.
func (c *Config) validate() error {
	if c.JWTAccessSecret == c.JWTRefreshSecret {
		return fmt.Errorf("JWT_ACCESS_SECRET and JWT_REFRESH_SECRET must differ")
	}
	// Unencrypted DB connections are a development-only convenience; fail fast
	// rather than ship payment data over plaintext in production.
	if c.IsProduction() && strings.Contains(c.DatabaseURL, "sslmode=disable") {
		return fmt.Errorf("DATABASE_URL must not use sslmode=disable when ENV=production")
	}
	const minSecretLen = 32
	for name, secret := range map[string]string{
		"JWT_ACCESS_SECRET":      c.JWTAccessSecret,
		"JWT_REFRESH_SECRET":     c.JWTRefreshSecret,
		"API_KEY_HMAC_SECRET":    c.APIKeyHMACSecret,
		"WEBHOOK_SIGNING_SECRET": c.WebhookSigningSecret,
	} {
		if len(secret) < minSecretLen {
			return fmt.Errorf("%s must be at least %d characters", name, minSecretLen)
		}
	}
	return nil
}
