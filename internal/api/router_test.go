package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/yahya-elkady/ledger/internal/api"
	"github.com/yahya-elkady/ledger/internal/api/handlers"
	"github.com/yahya-elkady/ledger/internal/api/middleware"
	"github.com/yahya-elkady/ledger/internal/auth"
	"github.com/yahya-elkady/ledger/internal/ratelimit"
)

// testRouter builds a router backed by miniredis with minimal deps — enough to
// exercise wiring concerns (routing, auth gating, CORS, 404/405) without stores.
func testRouter(t *testing.T, origins []string) http.Handler {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	jwtMgr, err := auth.NewJWTManager("access-secret-thirty-two-chars-min!!", "refresh-secret-thirty-two-chars-min", 15*time.Minute, 24*time.Hour)
	if err != nil {
		t.Fatalf("NewJWTManager: %v", err)
	}
	hasher := auth.NewAPIKeyHasher("api-key-hmac-secret-thirty-two-chars")

	authn := middleware.NewAuthenticator(nil, jwtMgr, hasher, rdb)
	rl := middleware.NewRateLimitMiddleware(ratelimit.NewRateLimiter(rdb), 1000, 100, 300)
	idem := middleware.NewIdempotency(rdb)
	h := handlers.New(handlers.Deps{JWT: jwtMgr, Hasher: hasher})

	return api.NewRouter(api.RouterDeps{
		Handlers:       h,
		Auth:           authn,
		RateLimit:      rl,
		Idempotency:    idem,
		AllowedOrigins: origins,
		Health: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok","db":"ok","redis":"ok"}`))
		},
		AuthRatePerMin: 10,
	})
}

func decodeErr(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil {
		t.Fatalf("decoding error body: %v\nbody: %s", err, rr.Body.String())
	}
	return v
}

func do(t *testing.T, r http.Handler, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

func TestHealthWired(t *testing.T) {
	r := testRouter(t, nil)
	rr := do(t, r, http.MethodGet, "/health", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /health = %d, want 200", rr.Code)
	}
}

func TestNotFoundJSON(t *testing.T) {
	r := testRouter(t, nil)
	rr := do(t, r, http.MethodGet, "/v1/does-not-exist", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	if got := decodeErr(t, rr)["error"]; got != "not_found" {
		t.Errorf("error = %v, want not_found", got)
	}
}

func TestMethodNotAllowedJSON(t *testing.T) {
	r := testRouter(t, nil)
	// /health is GET-only; POST should yield a JSON 405.
	rr := do(t, r, http.MethodPost, "/health", nil)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
	if got := decodeErr(t, rr)["error"]; got != "method_not_allowed" {
		t.Errorf("error = %v, want method_not_allowed", got)
	}
}

func TestUnauthenticatedRoutesReturn401(t *testing.T) {
	r := testRouter(t, nil)
	cases := []struct{ method, path string }{
		{http.MethodGet, "/v1/charges"},            // API key required
		{http.MethodGet, "/v1/payouts"},            // API key admin
		{http.MethodGet, "/v1/apikeys"},            // JWT required
		{http.MethodGet, "/v1/dashboard/overview"}, // JWT required
	}
	for _, c := range cases {
		rr := do(t, r, c.method, c.path, nil)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", c.method, c.path, rr.Code)
		}
	}
}

func TestMetricsEndpoint(t *testing.T) {
	r := testRouter(t, nil)
	// Drive a request so the logger middleware records an http_requests_total.
	do(t, r, http.MethodGet, "/v1/charges", nil)

	rr := do(t, r, http.MethodGet, "/metrics", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "http_requests_total") {
		t.Error("/metrics output missing http_requests_total")
	}
}

func TestCORSPreflight(t *testing.T) {
	r := testRouter(t, []string{"https://app.example.com"})

	// Allowed origin -> 204 with echoed origin.
	rr := do(t, r, http.MethodOptions, "/v1/charges", map[string]string{"Origin": "https://app.example.com"})
	if rr.Code != http.StatusNoContent {
		t.Errorf("allowed preflight = %d, want 204", rr.Code)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Allow-Origin = %q, want the allowed origin", got)
	}

	// Disallowed origin -> 403, no allow-origin header.
	rr = do(t, r, http.MethodOptions, "/v1/charges", map[string]string{"Origin": "https://evil.example.com"})
	if rr.Code != http.StatusForbidden {
		t.Errorf("disallowed preflight = %d, want 403", rr.Code)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Allow-Origin = %q, want empty for disallowed origin", got)
	}
}
