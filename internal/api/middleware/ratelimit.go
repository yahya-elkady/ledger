package middleware

import (
	"context"
	"math"
	"net"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog/log"

	"github.com/yahya-elkady/ledger/internal/api/respond"
	"github.com/yahya-elkady/ledger/internal/metrics"
	"github.com/yahya-elkady/ledger/internal/ratelimit"
)

// rateLimitWindow is the trailing window all per-minute limits are measured over.
const rateLimitWindow = 60 // seconds

// RateLimitMiddleware applies per-identity sliding-window rate limits and sets
// the standard X-RateLimit-* / Retry-After headers.
//
// Ordering note (deviation from build.md's listed stack order): this runs AFTER
// authentication for protected routes, because the limit and the bucket depend
// on the authenticated identity (API key id + mode, or dashboard merchant). For
// the unauthenticated auth routes, PerIP provides a fixed per-IP limit.
type RateLimitMiddleware struct {
	limiter      *ratelimit.RateLimiter
	liveRPM      int
	testRPM      int
	dashboardRPM int
}

// NewRateLimitMiddleware wires the middleware to the limiter and the per-minute
// limits from config.
func NewRateLimitMiddleware(limiter *ratelimit.RateLimiter, liveRPM, testRPM, dashboardRPM int) *RateLimitMiddleware {
	return &RateLimitMiddleware{
		limiter:      limiter,
		liveRPM:      liveRPM,
		testRPM:      testRPM,
		dashboardRPM: dashboardRPM,
	}
}

// Handler rate-limits per authenticated identity. API key clients are limited by
// key id at the live/test rate; dashboard (JWT) clients by merchant at the
// dashboard rate; any unauthenticated request falls back to a per-IP bucket.
func (m *RateLimitMiddleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, limit := m.subjectAndLimit(r)
		m.enforce(w, r, next, key, limit)
	})
}

// PerIP returns middleware enforcing a fixed limit per client IP, for
// unauthenticated routes (e.g. register/login at 10/min per IP).
func (m *RateLimitMiddleware) PerIP(limit int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			m.enforce(w, r, next, "rl:ip:"+clientIP(r), limit)
		})
	}
}

// subjectAndLimit derives the rate-limit bucket key and limit from the request's
// authenticated context.
func (m *RateLimitMiddleware) subjectAndLimit(r *http.Request) (string, int) {
	ctx := r.Context()
	switch Principal(ctx) {
	case PrincipalAPIKey:
		limit := m.testRPM
		if Mode(ctx) == "live" {
			limit = m.liveRPM
		}
		return "rl:apikey:" + APIKeyID(ctx), limit
	case PrincipalJWT:
		return "rl:dashboard:" + MerchantID(ctx), m.dashboardRPM
	default:
		return "rl:ip:" + clientIP(r), m.dashboardRPM
	}
}

// clientType classifies the request's principal for the rate-limit metric:
// "api_key", "dashboard", or "ip" for unauthenticated traffic.
func clientType(ctx context.Context) string {
	switch Principal(ctx) {
	case PrincipalAPIKey:
		return "api_key"
	case PrincipalJWT:
		return "dashboard"
	default:
		return "ip"
	}
}

// enforce runs the limiter for key/limit, sets headers, and either forwards to
// next or returns 429.
func (m *RateLimitMiddleware) enforce(w http.ResponseWriter, r *http.Request, next http.Handler, key string, limit int) {
	allowed, info, err := m.limiter.Allow(r.Context(), key, limit, rateLimitWindow)
	if err != nil {
		// Fail open: a Redis outage must not take down the whole API. Log and
		// allow the request through rather than rejecting legitimate traffic.
		log.Ctx(r.Context()).Error().Err(err).Msg("rate limiter unavailable, failing open")
		next.ServeHTTP(w, r)
		return
	}

	setRateLimitHeaders(w, info)

	if !allowed {
		metrics.RateLimitHit(clientType(r.Context()), Mode(r.Context()))
		// Abuse-detection signal: who is hammering, and at what limit.
		log.Ctx(r.Context()).Warn().Str("bucket", key).Int("limit", limit).
			Str("client_type", clientType(r.Context())).Str("merchant_id", MerchantID(r.Context())).
			Msg("rate limit exceeded")
		retryAfter := int(math.Ceil(info.RetryAfter.Seconds()))
		if retryAfter < 1 {
			retryAfter = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		respond.JSON(w, r, http.StatusTooManyRequests, rateLimitBody{
			Error:      respond.CodeRateLimitExceeded,
			Message:    "rate limit exceeded",
			RetryAfter: retryAfter,
			RequestID:  middleware.GetReqID(r.Context()),
		})
		return
	}

	next.ServeHTTP(w, r)
}

// rateLimitBody is the 429 response body (build.md specifies error + retry_after).
type rateLimitBody struct {
	Error      string `json:"error"`
	Message    string `json:"message"`
	RetryAfter int    `json:"retry_after"`
	RequestID  string `json:"request_id,omitempty"`
}

// setRateLimitHeaders writes the informational X-RateLimit-* headers present on
// every response.
func setRateLimitHeaders(w http.ResponseWriter, info ratelimit.RateLimitInfo) {
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(info.Limit))
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(info.Remaining))
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(info.ResetAt.Unix(), 10))
}

// clientIP extracts the client IP from RemoteAddr (already normalized by chi's
// RealIP middleware), stripping any port.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
