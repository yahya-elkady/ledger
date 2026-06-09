// Package ratelimit implements a Redis-backed sliding-window rate limiter.
//
// Each identity (API key or dashboard merchant) maps to a Redis sorted set
// whose members are individual request timestamps. A request is admitted by, in
// one atomic Lua script: evicting timestamps older than the window, recording
// the new request, and counting what remains. Doing it in a single script makes
// the check-and-increment atomic across concurrent requests — no race where two
// requests both observe "under limit".
package ratelimit

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// slidingWindowScript is the atomic limiter step. KEYS[1] is the identity's set;
// ARGV is now(ms), window(ms), limit, unique-member. It returns {allowed, count}.
//
// The new request is recorded before the count is read (so the caller's own
// request is included), matching the reference algorithm: count > limit => deny.
var slidingWindowScript = redis.NewScript(`
local key    = KEYS[1]
local now    = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local limit  = tonumber(ARGV[3])
local member = ARGV[4]

redis.call('ZREMRANGEBYSCORE', key, 0, now - window)
redis.call('ZADD', key, now, member)
local count = redis.call('ZCARD', key)
redis.call('PEXPIRE', key, window)

local allowed = 0
if count <= limit then
  allowed = 1
end
return {allowed, count}
`)

// RateLimitInfo describes the limiter's verdict for one request, used to set the
// X-RateLimit-* and Retry-After response headers.
type RateLimitInfo struct {
	Limit      int
	Remaining  int
	ResetAt    time.Time
	RetryAfter time.Duration // > 0 only when the request was denied
}

// RateLimiter admits or rejects requests against per-identity sliding windows.
type RateLimiter struct {
	rdb     *redis.Client
	counter uint64 // disambiguates members within the same millisecond
}

// NewRateLimiter constructs a limiter over the given Redis client.
func NewRateLimiter(rdb *redis.Client) *RateLimiter {
	return &RateLimiter{rdb: rdb}
}

// Allow records a request against key and reports whether it is within limit
// over the trailing windowSeconds. It returns rate-limit metadata regardless of
// the verdict. A Redis error is returned as-is; callers decide whether to
// fail-open or fail-closed.
func (l *RateLimiter) Allow(ctx context.Context, key string, limit, windowSeconds int) (bool, RateLimitInfo, error) {
	now := time.Now()
	nowMs := now.UnixMilli()
	windowMs := int64(windowSeconds) * 1000

	// Unique member: timestamp plus a process-local counter, so two requests in
	// the same millisecond are recorded as distinct entries (a bare timestamp
	// member would collide and undercount).
	member := fmt.Sprintf("%d-%d", nowMs, atomic.AddUint64(&l.counter, 1))

	res, err := slidingWindowScript.Run(ctx, l.rdb, []string{key}, nowMs, windowMs, limit, member).Result()
	if err != nil {
		return false, RateLimitInfo{}, fmt.Errorf("rate limit script: %w", err)
	}

	allowed, count, err := parseScriptResult(res)
	if err != nil {
		return false, RateLimitInfo{}, err
	}

	remaining := limit - count
	if remaining < 0 {
		remaining = 0
	}
	resetAt := now.Add(time.Duration(windowMs) * time.Millisecond)

	info := RateLimitInfo{Limit: limit, Remaining: remaining, ResetAt: resetAt}
	if !allowed {
		info.RetryAfter = time.Until(resetAt)
	}
	return allowed, info, nil
}

// parseScriptResult unpacks the Lua {allowed, count} reply.
func parseScriptResult(res any) (allowed bool, count int, err error) {
	arr, ok := res.([]any)
	if !ok || len(arr) != 2 {
		return false, 0, fmt.Errorf("unexpected rate limit reply: %#v", res)
	}
	a, ok1 := arr[0].(int64)
	c, ok2 := arr[1].(int64)
	if !ok1 || !ok2 {
		return false, 0, fmt.Errorf("unexpected rate limit reply types: %#v", res)
	}
	return a == 1, int(c), nil
}
