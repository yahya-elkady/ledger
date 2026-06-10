package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// authedPOST builds a POST request already carrying an authenticated merchant.
func authedPOST(idemKey string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/charges", nil)
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	ctx := withAuth(req.Context(), "merchant-1", "live", []string{"write"}, PrincipalAPIKey)
	return req.WithContext(ctx)
}

func TestIdempotencyReplay(t *testing.T) {
	idem := NewIdempotency(newRedis(t))

	var calls int32
	handler := idem.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"id":"charge_%d"}`, n)
	}))

	// First request runs the handler and returns its response.
	rr1 := httptest.NewRecorder()
	handler.ServeHTTP(rr1, authedPOST("key-abc"))
	if rr1.Code != http.StatusCreated {
		t.Fatalf("first status = %d, want 201", rr1.Code)
	}
	if rr1.Header().Get("Idempotency-Replayed") == "true" {
		t.Error("first response should not be marked replayed")
	}
	body1 := rr1.Body.String()

	// Second request with the same key replays the cached response without
	// re-running the handler.
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, authedPOST("key-abc"))
	if rr2.Code != http.StatusCreated {
		t.Fatalf("replayed status = %d, want 201", rr2.Code)
	}
	if rr2.Header().Get("Idempotency-Replayed") != "true" {
		t.Error("second response should be marked Idempotency-Replayed: true")
	}
	if rr2.Body.String() != body1 {
		t.Errorf("replayed body = %q, want %q", rr2.Body.String(), body1)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("handler ran %d times, want 1 (second served from cache)", got)
	}
}

func TestIdempotencyKeyRequired(t *testing.T) {
	idem := NewIdempotency(newRedis(t))
	handler := idem.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, authedPOST("")) // no Idempotency-Key
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 when key missing", rr.Code)
	}
}

func TestIdempotencyDistinctKeys(t *testing.T) {
	idem := NewIdempotency(newRedis(t))
	var calls int32
	handler := idem.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusCreated)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), authedPOST("key-1"))
	handler.ServeHTTP(httptest.NewRecorder(), authedPOST("key-2"))
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("handler ran %d times, want 2 for distinct keys", got)
	}
}

func TestIdempotencySkipsNonPOST(t *testing.T) {
	idem := NewIdempotency(newRedis(t))
	called := false
	handler := idem.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	// A GET without an Idempotency-Key must pass straight through.
	req := httptest.NewRequest(http.MethodGet, "/v1/charges", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if !called || rr.Code != http.StatusOK {
		t.Errorf("GET should pass through: called=%v status=%d", called, rr.Code)
	}
}
