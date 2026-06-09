package ratelimit_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/yahya-elkady/ledger/internal/ratelimit"
)

func newLimiter(t *testing.T) (*ratelimit.RateLimiter, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return ratelimit.NewRateLimiter(rdb), mr
}

func TestAllowBoundary(t *testing.T) {
	l, _ := newLimiter(t)
	ctx := context.Background()
	const limit = 5

	// The first `limit` requests are admitted.
	for i := 1; i <= limit; i++ {
		allowed, info, err := l.Allow(ctx, "rl:test", limit, 60)
		if err != nil {
			t.Fatalf("Allow #%d: %v", i, err)
		}
		if !allowed {
			t.Fatalf("request #%d should be allowed (limit %d)", i, limit)
		}
		if info.Remaining != limit-i {
			t.Errorf("request #%d remaining = %d, want %d", i, info.Remaining, limit-i)
		}
	}

	// The very next request crosses the boundary and is denied.
	allowed, info, err := l.Allow(ctx, "rl:test", limit, 60)
	if err != nil {
		t.Fatalf("Allow over-limit: %v", err)
	}
	if allowed {
		t.Errorf("request #%d should be denied (limit %d)", limit+1, limit)
	}
	if info.Remaining != 0 {
		t.Errorf("remaining = %d, want 0 when over limit", info.Remaining)
	}
	if info.RetryAfter <= 0 {
		t.Errorf("RetryAfter = %v, want > 0 when denied", info.RetryAfter)
	}
}

func TestAllowSeparatesKeys(t *testing.T) {
	l, _ := newLimiter(t)
	ctx := context.Background()

	// Exhaust one identity.
	if allowed, _, _ := l.Allow(ctx, "rl:a", 1, 60); !allowed {
		t.Fatal("first request for a should be allowed")
	}
	if allowed, _, _ := l.Allow(ctx, "rl:a", 1, 60); allowed {
		t.Fatal("second request for a should be denied")
	}

	// A different identity is unaffected.
	if allowed, _, _ := l.Allow(ctx, "rl:b", 1, 60); !allowed {
		t.Error("first request for b should be allowed (separate window)")
	}
}

func TestAllowWindowSlides(t *testing.T) {
	l, mr := newLimiter(t)
	ctx := context.Background()

	if allowed, _, _ := l.Allow(ctx, "rl:slide", 1, 60); !allowed {
		t.Fatal("first request should be allowed")
	}
	if allowed, _, _ := l.Allow(ctx, "rl:slide", 1, 60); allowed {
		t.Fatal("second request in-window should be denied")
	}

	// Advance time beyond the window; the old timestamp ages out and the next
	// request is admitted again.
	mr.FastForward(61 * time.Second) // expire the PEXPIRE'd window key
	if allowed, _, _ := l.Allow(ctx, "rl:slide", 1, 60); !allowed {
		t.Error("request after window should be allowed again")
	}
}
