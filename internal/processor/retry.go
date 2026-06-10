package processor

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/rs/zerolog/log"
)

// RetryPolicy controls how transient processor failures are retried. build.md
// asks for "max 3 retries, exponential backoff" — MaxAttempts counts the first
// try plus retries.
type RetryPolicy struct {
	MaxAttempts int           // total attempts (1 try + up to 3 retries => 4)
	BaseDelay   time.Duration // delay before the first retry
	MaxDelay    time.Duration // ceiling for the backoff
}

// DefaultRetryPolicy is 1 initial attempt + 3 retries with exponential backoff.
var DefaultRetryPolicy = RetryPolicy{
	MaxAttempts: 4,
	BaseDelay:   200 * time.Millisecond,
	MaxDelay:    3 * time.Second,
}

// Retry runs fn, retrying transient failures (per isRetryable) with exponential
// backoff and jitter, until it succeeds, hits a non-retryable error, exhausts
// attempts, or the context is cancelled. It is generic over the call's result
// type so every adapter method can share one resilient loop. Vendor adapters
// flag transient errors via NewError(..., retryable=true).
func Retry[T any](ctx context.Context, p RetryPolicy, fn func() (T, error)) (T, error) {
	var zero T
	delay := p.BaseDelay
	for attempt := 1; ; attempt++ {
		result, err := fn()
		if err == nil || !isRetryable(err) {
			return result, err
		}
		if attempt >= p.MaxAttempts {
			// Retries exhausted: the caller surfaces the error to the client; the
			// trail of how many attempts were burned belongs here.
			log.Ctx(ctx).Error().Err(err).Int("attempts", attempt).
				Msg("processor call failed after exhausting retries")
			return result, err
		}

		// Sleep with jitter before the next attempt, honoring cancellation.
		// (math/rand jitter only spreads retry timing — it is not key material.)
		sleep := delay + time.Duration(rand.Int64N(int64(delay)+1))
		log.Ctx(ctx).Warn().Err(err).Int("attempt", attempt).Dur("backoff", sleep).
			Msg("transient processor error, retrying")
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(sleep):
		}
		if delay = delay * 2; delay > p.MaxDelay {
			delay = p.MaxDelay
		}
	}
}
