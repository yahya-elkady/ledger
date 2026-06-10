//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ory/dockertest/v3"
	"github.com/redis/go-redis/v9"

	"github.com/yahya-elkady/ledger/internal/api"
	"github.com/yahya-elkady/ledger/internal/api/handlers"
	"github.com/yahya-elkady/ledger/internal/api/middleware"
	"github.com/yahya-elkady/ledger/internal/auth"
	"github.com/yahya-elkady/ledger/internal/processor"
	"github.com/yahya-elkady/ledger/internal/ratelimit"
	"github.com/yahya-elkady/ledger/internal/store"
	"github.com/yahya-elkady/ledger/internal/webhook"
)

// Shared infrastructure for the suite, set up once in TestMain.
var (
	dbPool    *pgxpool.Pool
	redisAddr string
)

const (
	testAccessSecret  = "integration-access-secret-thirty-two!"
	testRefreshSecret = "integration-refresh-secret-thirty-two"
	testHMACSecret    = "integration-api-key-hmac-secret-32chars"
)

// TestMain spins up throwaway Postgres + Redis containers, applies the
// migrations, and shares the connections with the tests. If no Docker daemon is
// reachable, the whole suite is skipped (exit 0) rather than failed.
func TestMain(m *testing.M) {
	pool, err := dockertest.NewPool("")
	if err != nil {
		fmt.Println("integration: cannot construct docker pool, skipping:", err)
		os.Exit(0)
	}
	if err := pool.Client.Ping(); err != nil {
		fmt.Println("integration: docker daemon not reachable, skipping:", err)
		os.Exit(0)
	}
	pool.MaxWait = 120 * time.Second

	ctx := context.Background()

	pg, err := pool.Run("postgres", "16-alpine", []string{
		"POSTGRES_PASSWORD=secret",
		"POSTGRES_DB=payments_test",
	})
	if err != nil {
		fmt.Println("integration: could not start postgres:", err)
		os.Exit(1)
	}
	_ = pg.Expire(600) // self-destruct safeguard

	rd, err := pool.Run("redis", "7-alpine", nil)
	if err != nil {
		_ = pool.Purge(pg)
		fmt.Println("integration: could not start redis:", err)
		os.Exit(1)
	}
	_ = rd.Expire(600)
	redisAddr = rd.GetHostPort("6379/tcp")

	dsn := fmt.Sprintf("postgres://postgres:secret@%s/payments_test?sslmode=disable", pg.GetHostPort("5432/tcp"))

	if err := pool.Retry(func() error {
		p, err := pgxpool.New(ctx, dsn)
		if err != nil {
			return err
		}
		if err := p.Ping(ctx); err != nil {
			p.Close()
			return err
		}
		dbPool = p
		return nil
	}); err != nil {
		_ = pool.Purge(pg)
		_ = pool.Purge(rd)
		fmt.Println("integration: postgres never became ready:", err)
		os.Exit(1)
	}

	// Redis readiness.
	if err := pool.Retry(func() error {
		c := redis.NewClient(&redis.Options{Addr: redisAddr})
		defer c.Close()
		return c.Ping(ctx).Err()
	}); err != nil {
		_ = pool.Purge(pg)
		_ = pool.Purge(rd)
		fmt.Println("integration: redis never became ready:", err)
		os.Exit(1)
	}

	if err := applyMigrations(ctx, dbPool); err != nil {
		_ = pool.Purge(pg)
		_ = pool.Purge(rd)
		fmt.Println("integration: migrations failed:", err)
		os.Exit(1)
	}

	code := m.Run()

	dbPool.Close()
	_ = pool.Purge(pg)
	_ = pool.Purge(rd)
	os.Exit(code)
}

// applyMigrations runs every migrations/*.sql file in lexical order. Each file
// is a multi-statement script executed via the simple protocol (no args).
func applyMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	dir := filepath.Join("..", "..", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("reading migrations dir: %w", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".sql" {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	for _, name := range files {
		sqlBytes, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("reading %s: %w", name, err)
		}
		if _, err := pool.Exec(ctx, string(sqlBytes)); err != nil {
			return fmt.Errorf("applying %s: %w", name, err)
		}
	}
	return nil
}

// newServer builds an httptest server fronting the real stack with the given
// rate limits (per minute). A fresh Redis client is used so each server has its
// own connection pool; the underlying Redis (and DB) are shared.
func newServer(t *testing.T, liveRPM, testRPM, dashRPM int) *httptest.Server {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	t.Cleanup(func() { _ = rdb.Close() })

	jwtMgr, err := auth.NewJWTManager(testAccessSecret, testRefreshSecret, 15*time.Minute, 24*time.Hour)
	if err != nil {
		t.Fatalf("NewJWTManager: %v", err)
	}
	hasher := auth.NewAPIKeyHasher(testHMACSecret)
	authStore := store.NewAuthStore(dbPool)
	billing := store.NewBillingStore(dbPool)
	payouts := store.NewPayoutStore(dbPool)

	h := handlers.New(handlers.Deps{
		Merchants:     store.NewMerchantStore(dbPool),
		APIKeys:       authStore,
		Tokens:        authStore,
		Customers:     store.NewCustomerStore(dbPool),
		Charges:       store.NewChargeStore(dbPool),
		Plans:         billing,
		Subscriptions: billing,
		BankAccounts:  payouts,
		Payouts:       payouts,
		Dashboard:     store.NewDashboardStore(dbPool),
		Audit:         store.NewAuditStore(dbPool),
		Processor:     &processor.Fake{},
		StripeWebhook: &webhook.Fake{},
		JWT:           jwtMgr,
		Hasher:        hasher,
		AccessTTL:     15 * time.Minute,
	})

	router := api.NewRouter(api.RouterDeps{
		Handlers:    h,
		Auth:        middleware.NewAuthenticator(authStore, jwtMgr, hasher, rdb),
		RateLimit:   middleware.NewRateLimitMiddleware(ratelimit.NewRateLimiter(rdb), liveRPM, testRPM, dashRPM),
		Idempotency: middleware.NewIdempotency(rdb),
		Health: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		},
		AuthRatePerMin: 100,
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv
}

// --- HTTP helpers ----------------------------------------------------------

func doReq(t *testing.T, srv *httptest.Server, method, path, bearer string, headers map[string]string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&decoded) // empty bodies (204) decode to nil
	return resp, decoded
}

var emailSeq int

// registerMerchant creates a unique merchant and returns its access token + id.
func registerMerchant(t *testing.T, srv *httptest.Server) (accessToken, merchantID string) {
	t.Helper()
	emailSeq++
	email := fmt.Sprintf("merchant%d-%d@itest.local", emailSeq, time.Now().UnixNano())
	resp, body := doReq(t, srv, http.MethodPost, "/v1/auth/register", "", nil, map[string]any{
		"email":         email,
		"password":      "hunter2hunter",
		"business_name": "Integration Shop",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: status %d, body %v", resp.StatusCode, body)
	}
	token, _ := body["access_token"].(string)
	if token == "" {
		t.Fatalf("register: no access_token in %v", body)
	}
	m, _ := body["merchant"].(map[string]any)
	id, _ := m["id"].(string)
	return token, id
}

// createAPIKey mints a secret key of the given mode/scope and returns its
// plaintext. Requires a dashboard access token.
func createAPIKey(t *testing.T, srv *httptest.Server, accessToken, mode string, scope []string) string {
	t.Helper()
	resp, body := doReq(t, srv, http.MethodPost, "/v1/apikeys", accessToken,
		map[string]string{"Idempotency-Key": fmt.Sprintf("apikey-%d-%d", emailSeq, time.Now().UnixNano())},
		map[string]any{"name": "itest", "type": "secret", "mode": mode, "scope": scope})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create api key: status %d, body %v", resp.StatusCode, body)
	}
	key, _ := body["key"].(string)
	if key == "" {
		t.Fatalf("create api key: no plaintext key in %v", body)
	}
	return key
}

// --- tests -----------------------------------------------------------------

func TestRegisterAndLogin(t *testing.T) {
	srv := newServer(t, 1000, 1000, 1000)
	emailSeq++
	email := fmt.Sprintf("login%d-%d@itest.local", emailSeq, time.Now().UnixNano())

	resp, body := doReq(t, srv, http.MethodPost, "/v1/auth/register", "", nil, map[string]any{
		"email": email, "password": "hunter2hunter", "business_name": "Login Co",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register status %d: %v", resp.StatusCode, body)
	}

	resp, body = doReq(t, srv, http.MethodPost, "/v1/auth/login", "", nil, map[string]any{
		"email": email, "password": "hunter2hunter",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status %d: %v", resp.StatusCode, body)
	}
	if body["access_token"] == "" || body["refresh_token"] == "" {
		t.Error("login did not return both tokens")
	}

	// Wrong password -> generic 401.
	resp, _ = doReq(t, srv, http.MethodPost, "/v1/auth/login", "", nil, map[string]any{
		"email": email, "password": "wrongpassword",
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad-password login status = %d, want 401", resp.StatusCode)
	}
}

func TestAPIKeyGenerationAndUse(t *testing.T) {
	srv := newServer(t, 1000, 1000, 1000)
	access, _ := registerMerchant(t, srv)
	key := createAPIKey(t, srv, access, "test", []string{"write"})

	// Use the API key to create a customer.
	resp, body := doReq(t, srv, http.MethodPost, "/v1/customers", key,
		map[string]string{"Idempotency-Key": "cust-" + key[len(key)-8:]},
		map[string]any{"email": "buyer@itest.local", "name": "Buyer"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create customer with api key: status %d, body %v", resp.StatusCode, body)
	}
	if body["id"] == "" {
		t.Error("created customer missing id")
	}

	// A bogus key is rejected.
	resp, _ = doReq(t, srv, http.MethodGet, "/v1/customers", "sk_test_boguskeyvalue", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bogus key status = %d, want 401", resp.StatusCode)
	}
}

func TestChargeFlowPersists(t *testing.T) {
	srv := newServer(t, 1000, 1000, 1000)
	access, _ := registerMerchant(t, srv)
	key := createAPIKey(t, srv, access, "test", []string{"write"})

	resp, body := doReq(t, srv, http.MethodPost, "/v1/charges", key,
		map[string]string{"Idempotency-Key": "charge-" + key[len(key)-8:]},
		map[string]any{"amount": 1999, "currency": "USD", "payment_method_id": "pm_tok", "processor": "stripe"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create charge: status %d, body %v", resp.StatusCode, body)
	}
	chargeID, _ := body["id"].(string)
	if body["status"] != "succeeded" {
		t.Errorf("charge status = %v, want succeeded", body["status"])
	}

	// Verify it is readable back and persisted in the DB.
	resp, _ = doReq(t, srv, http.MethodGet, "/v1/charges/"+chargeID, key, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("get charge status = %d, want 200", resp.StatusCode)
	}
	var count int
	if err := dbPool.QueryRow(context.Background(),
		"SELECT count(*) FROM charges WHERE id = $1", chargeID).Scan(&count); err != nil {
		t.Fatalf("db count: %v", err)
	}
	if count != 1 {
		t.Errorf("charges rows for id = %d, want 1", count)
	}
}

func TestIdempotentChargeIsSingleRow(t *testing.T) {
	srv := newServer(t, 1000, 1000, 1000)
	access, _ := registerMerchant(t, srv)
	key := createAPIKey(t, srv, access, "test", []string{"write"})
	idemKey := "idem-" + key[len(key)-8:]

	mk := func() (*http.Response, map[string]any) {
		return doReq(t, srv, http.MethodPost, "/v1/charges", key,
			map[string]string{"Idempotency-Key": idemKey},
			map[string]any{"amount": 2500, "currency": "USD", "payment_method_id": "pm_tok", "processor": "stripe"})
	}

	resp1, body1 := mk()
	if resp1.StatusCode != http.StatusCreated {
		t.Fatalf("first charge status %d: %v", resp1.StatusCode, body1)
	}
	firstID, _ := body1["id"].(string)

	resp2, body2 := mk()
	if resp2.Header.Get("Idempotency-Replayed") != "true" {
		t.Errorf("replay header = %q, want true", resp2.Header.Get("Idempotency-Replayed"))
	}
	if id2, _ := body2["id"].(string); id2 != firstID {
		t.Errorf("replayed id = %q, want %q", id2, firstID)
	}

	// Exactly one row despite two requests.
	var count int
	if err := dbPool.QueryRow(context.Background(),
		"SELECT count(*) FROM charges WHERE idempotency_key = $1", idemKey).Scan(&count); err != nil {
		t.Fatalf("db count: %v", err)
	}
	if count != 1 {
		t.Errorf("charge rows for idempotency key = %d, want 1", count)
	}
}

func TestRateLimitReturns429(t *testing.T) {
	// Tiny dashboard limit so a short loop trips it.
	srv := newServer(t, 1000, 1000, 3)
	access, _ := registerMerchant(t, srv)

	var got429 bool
	var lastResp *http.Response
	for i := 0; i < 10; i++ {
		resp, _ := doReq(t, srv, http.MethodGet, "/v1/dashboard/overview", access, nil, nil)
		lastResp = resp
		if resp.StatusCode == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatalf("never hit 429 within 10 requests (limit 3)")
	}
	if lastResp.Header.Get("Retry-After") == "" {
		t.Error("429 missing Retry-After header")
	}
	if lastResp.Header.Get("X-RateLimit-Limit") == "" {
		t.Error("429 missing X-RateLimit-Limit header")
	}
}

func TestModeIsolationAcrossKeys(t *testing.T) {
	srv := newServer(t, 1000, 1000, 1000)
	access, _ := registerMerchant(t, srv)
	liveKey := createAPIKey(t, srv, access, "live", []string{"write"})
	testKey := createAPIKey(t, srv, access, "test", []string{"write"})

	// Create a charge with the LIVE key.
	resp, body := doReq(t, srv, http.MethodPost, "/v1/charges", liveKey,
		map[string]string{"Idempotency-Key": "live-charge-" + liveKey[len(liveKey)-8:]},
		map[string]any{"amount": 4242, "currency": "USD", "payment_method_id": "pm_tok", "processor": "stripe"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("live charge: status %d, body %v", resp.StatusCode, body)
	}
	liveChargeID, _ := body["id"].(string)

	// The TEST key must not see it.
	resp, _ = doReq(t, srv, http.MethodGet, "/v1/charges/"+liveChargeID, testKey, nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("test key GET live charge = %d, want 404", resp.StatusCode)
	}

	// The LIVE key still sees it.
	resp, _ = doReq(t, srv, http.MethodGet, "/v1/charges/"+liveChargeID, liveKey, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("live key GET live charge = %d, want 200", resp.StatusCode)
	}
}
