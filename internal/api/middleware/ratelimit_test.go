package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yahya-elkady/ledger/internal/ratelimit"
)

func TestRateLimitMiddleware(t *testing.T) {
	rdb := newRedis(t)
	// liveRPM=2, testRPM=1, dashboardRPM=5.
	m := NewRateLimitMiddleware(ratelimit.NewRateLimiter(rdb), 2, 1, 5)

	handler := m.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Authenticated API key in test mode → limit 1.
	req := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		ctx := withAuth(r.Context(), "m", "test", []string{"read"}, PrincipalAPIKey)
		ctx = withAPIKeyID(ctx, "key-xyz")
		return r.WithContext(ctx)
	}

	// First request allowed, with informational headers.
	rr1 := httptest.NewRecorder()
	handler.ServeHTTP(rr1, req())
	if rr1.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200", rr1.Code)
	}
	if rr1.Header().Get("X-RateLimit-Limit") != "1" {
		t.Errorf("X-RateLimit-Limit = %q, want 1", rr1.Header().Get("X-RateLimit-Limit"))
	}
	if rr1.Header().Get("X-RateLimit-Remaining") != "0" {
		t.Errorf("X-RateLimit-Remaining = %q, want 0", rr1.Header().Get("X-RateLimit-Remaining"))
	}

	// Second request over the test-mode limit → 429 with Retry-After.
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req())
	if rr2.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want 429", rr2.Code)
	}
	if rr2.Header().Get("Retry-After") == "" {
		t.Error("Retry-After header should be set on 429")
	}
	if ct := rr2.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestRateLimitPerIP(t *testing.T) {
	rdb := newRedis(t)
	m := NewRateLimitMiddleware(ratelimit.NewRateLimiter(rdb), 1000, 100, 300)
	handler := m.PerIP(1)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest(http.MethodPost, "/v1/auth/login", nil)
	r.RemoteAddr = "203.0.113.7:54321"

	rr1 := httptest.NewRecorder()
	handler.ServeHTTP(rr1, r)
	if rr1.Code != http.StatusOK {
		t.Fatalf("first per-IP status = %d, want 200", rr1.Code)
	}

	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, r)
	if rr2.Code != http.StatusTooManyRequests {
		t.Errorf("second per-IP status = %d, want 429", rr2.Code)
	}
}
