package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/yahya-elkady/ledger/internal/api/handlers"
	"github.com/yahya-elkady/ledger/internal/api/middleware"
	"github.com/yahya-elkady/ledger/internal/auth"
)

const (
	hmacSecret    = "test_hmac_secret_at_least_32_chars_long_xx"
	accessSecret  = "access_secret_at_least_32_chars_long_aaaa"
	refreshSecret = "refresh_secret_at_least_32_chars_long_bbb"
)

type harness struct {
	h         *handlers.Handlers
	merchants *fakeMerchants
	apiKeys   *fakeAPIKeys
	tokens    *fakeTokens
	customers *fakeCustomers
	audit     *fakeAudit
	jwt       *auth.JWTManager
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	jwtMgr, err := auth.NewJWTManager(accessSecret, refreshSecret, 15*time.Minute, 24*time.Hour)
	if err != nil {
		t.Fatalf("NewJWTManager: %v", err)
	}
	m := newFakeMerchants()
	ak := newFakeAPIKeys()
	tk := newFakeTokens()
	cu := newFakeCustomers()
	au := &fakeAudit{}
	h := handlers.New(m, ak, tk, cu, au, handlers.Config{
		JWT:       jwtMgr,
		Hasher:    auth.NewAPIKeyHasher(hmacSecret),
		AccessTTL: 15 * time.Minute,
	})
	return &harness{h: h, merchants: m, apiKeys: ak, tokens: tk, customers: cu, audit: au, jwt: jwtMgr}
}

// req builds a request carrying a dashboard (JWT) principal for merchantID, by
// minting a real access token and running it through the actual JWTMiddleware —
// so the handler sees exactly the context the middleware would inject.
func (h *harness) req(method, target, body, merchantID string) *http.Request {
	tok, err := h.jwt.IssueAccessToken(merchantID, "test", []string{"admin"})
	if err != nil {
		panic(err)
	}
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+tok)

	a := middleware.NewAuthenticator(nil, h.jwt, nil, nil)
	var out *http.Request
	a.JWTMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, rr *http.Request) { out = rr })).
		ServeHTTP(httptest.NewRecorder(), r)
	return out
}

func withURLParam(r *http.Request, key, val string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, val)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func decode[T any](t *testing.T, rr *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(rr.Body).Decode(&v); err != nil {
		t.Fatalf("decoding response: %v\nbody: %s", err, rr.Body.String())
	}
	return v
}

// --- auth flow -------------------------------------------------------------

func TestRegisterLoginRefreshLogout(t *testing.T) {
	h := newHarness(t)

	// Register.
	rr := httptest.NewRecorder()
	body := `{"email":"Owner@Shop.com","password":"hunter2hunter","business_name":"Shop"}`
	h.h.Register(rr, httptest.NewRequest(http.MethodPost, "/v1/auth/register", strings.NewReader(body)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("register status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	reg := decode[map[string]any](t, rr)
	if reg["access_token"] == "" || reg["refresh_token"] == "" {
		t.Fatal("register should return tokens")
	}
	// Email normalized to lowercase.
	if _, _, err := h.merchants.GetMerchantByEmail(context.Background(), "owner@shop.com"); err != nil {
		t.Errorf("merchant should be stored under normalized email: %v", err)
	}

	// Duplicate registration → 409.
	rr = httptest.NewRecorder()
	h.h.Register(rr, httptest.NewRequest(http.MethodPost, "/v1/auth/register", strings.NewReader(body)))
	if rr.Code != http.StatusConflict {
		t.Errorf("duplicate register status = %d, want 409", rr.Code)
	}

	// Login with correct password.
	rr = httptest.NewRecorder()
	h.h.Login(rr, httptest.NewRequest(http.MethodPost, "/v1/auth/login",
		strings.NewReader(`{"email":"owner@shop.com","password":"hunter2hunter"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200", rr.Code)
	}
	login := decode[map[string]any](t, rr)
	refreshTok, _ := login["refresh_token"].(string)
	if refreshTok == "" {
		t.Fatal("login should return a refresh token")
	}

	// Wrong password → 401.
	rr = httptest.NewRecorder()
	h.h.Login(rr, httptest.NewRequest(http.MethodPost, "/v1/auth/login",
		strings.NewReader(`{"email":"owner@shop.com","password":"wrongpassword"}`)))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("bad-password login status = %d, want 401", rr.Code)
	}

	// Refresh rotates the token.
	rr = httptest.NewRecorder()
	h.h.Refresh(rr, httptest.NewRequest(http.MethodPost, "/v1/auth/refresh",
		strings.NewReader(`{"refresh_token":"`+refreshTok+`"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("refresh status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	rotated := decode[map[string]any](t, rr)
	newRefresh, _ := rotated["refresh_token"].(string)
	if newRefresh == "" || newRefresh == refreshTok {
		t.Error("refresh should return a new, different refresh token")
	}

	// Reusing the OLD refresh token after rotation → 401.
	rr = httptest.NewRecorder()
	h.h.Refresh(rr, httptest.NewRequest(http.MethodPost, "/v1/auth/refresh",
		strings.NewReader(`{"refresh_token":"`+refreshTok+`"}`)))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("reused refresh token status = %d, want 401", rr.Code)
	}

	// Logout the new token → 204.
	rr = httptest.NewRecorder()
	h.h.Logout(rr, httptest.NewRequest(http.MethodPost, "/v1/auth/logout",
		strings.NewReader(`{"refresh_token":"`+newRefresh+`"}`)))
	if rr.Code != http.StatusNoContent {
		t.Errorf("logout status = %d, want 204", rr.Code)
	}
}

func TestRegisterValidation(t *testing.T) {
	h := newHarness(t)
	cases := map[string]string{
		"bad email":      `{"email":"nope","password":"hunter2hunter","business_name":"S"}`,
		"short password": `{"email":"a@b.com","password":"short","business_name":"S"}`,
		"missing name":   `{"email":"a@b.com","password":"hunter2hunter","business_name":""}`,
		"unknown field":  `{"email":"a@b.com","password":"hunter2hunter","business_name":"S","x":1}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.h.Register(rr, httptest.NewRequest(http.MethodPost, "/v1/auth/register", strings.NewReader(body)))
			if rr.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rr.Code)
			}
		})
	}
}

// --- api keys --------------------------------------------------------------

func TestAPIKeyLifecycle(t *testing.T) {
	h := newHarness(t)
	const merchantID = "11111111-1111-1111-1111-000000000001"

	// Create.
	rr := httptest.NewRecorder()
	req := h.req(http.MethodPost, "/v1/apikeys", `{"name":"server","type":"secret","mode":"live","scope":["write"]}`, merchantID)
	h.h.CreateAPIKey(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create apikey status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	created := decode[map[string]any](t, rr)
	plaintext, _ := created["key"].(string)
	if !strings.HasPrefix(plaintext, "sk_live_") {
		t.Errorf("plaintext key = %q, want sk_live_ prefix", plaintext)
	}
	keyID, _ := created["id"].(string)

	// List shows it (without the plaintext or hash).
	rr = httptest.NewRecorder()
	h.h.ListAPIKeys(rr, h.req(http.MethodGet, "/v1/apikeys", "", merchantID))
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", rr.Code)
	}
	if strings.Contains(rr.Body.String(), plaintext) || strings.Contains(rr.Body.String(), "key_hash") {
		t.Error("list must not leak plaintext or hash")
	}

	// Revoke.
	rr = httptest.NewRecorder()
	req = withURLParam(h.req(http.MethodDelete, "/v1/apikeys/"+keyID, "", merchantID), "id", keyID)
	h.h.DeleteAPIKey(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d, want 204", rr.Code)
	}

	// After revoke, it's gone from the active list.
	rr = httptest.NewRecorder()
	h.h.ListAPIKeys(rr, h.req(http.MethodGet, "/v1/apikeys", "", merchantID))
	list := decode[map[string]any](t, rr)
	if data, _ := list["data"].([]any); len(data) != 0 {
		t.Errorf("active keys after revoke = %d, want 0", len(data))
	}

	// Audit recorded create + revoke.
	got := strings.Join(h.audit.actions(), ",")
	if !strings.Contains(got, "apikey.created") || !strings.Contains(got, "apikey.revoked") {
		t.Errorf("audit actions = %q, want create+revoke", got)
	}
}

func TestCreateAPIKeyValidation(t *testing.T) {
	h := newHarness(t)
	const merchantID = "11111111-1111-1111-1111-000000000001"
	rr := httptest.NewRecorder()
	req := h.req(http.MethodPost, "/v1/apikeys", `{"name":"k","type":"bogus","mode":"live","scope":["write"]}`, merchantID)
	h.h.CreateAPIKey(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for bad type", rr.Code)
	}
}

// --- customers -------------------------------------------------------------

func TestCustomerCRUD(t *testing.T) {
	h := newHarness(t)
	const merchantID = "11111111-1111-1111-1111-000000000009"

	// Create.
	rr := httptest.NewRecorder()
	h.h.CreateCustomer(rr, h.req(http.MethodPost, "/v1/customers", `{"email":"c@x.com","name":"Cust","metadata":{"tier":"gold"}}`, merchantID))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	created := decode[map[string]any](t, rr)
	id, _ := created["id"].(string)

	// Get.
	rr = httptest.NewRecorder()
	h.h.GetCustomer(rr, withURLParam(h.req(http.MethodGet, "/v1/customers/"+id, "", merchantID), "id", id))
	if rr.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200", rr.Code)
	}

	// Get with a different merchant → 404 (isolation).
	rr = httptest.NewRecorder()
	h.h.GetCustomer(rr, withURLParam(h.req(http.MethodGet, "/v1/customers/"+id, "", "99999999-9999-9999-9999-999999999999"), "id", id))
	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-merchant get status = %d, want 404", rr.Code)
	}

	// Update.
	rr = httptest.NewRecorder()
	h.h.UpdateCustomer(rr, withURLParam(h.req(http.MethodPut, "/v1/customers/"+id, `{"email":"c2@x.com","name":"New"}`, merchantID), "id", id))
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200", rr.Code)
	}
	updated := decode[map[string]any](t, rr)
	if updated["email"] != "c2@x.com" {
		t.Errorf("email = %v, want c2@x.com", updated["email"])
	}

	// List.
	rr = httptest.NewRecorder()
	h.h.ListCustomers(rr, h.req(http.MethodGet, "/v1/customers", "", merchantID))
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", rr.Code)
	}

	// Delete.
	rr = httptest.NewRecorder()
	h.h.DeleteCustomer(rr, withURLParam(h.req(http.MethodDelete, "/v1/customers/"+id, "", merchantID), "id", id))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", rr.Code)
	}

	// Get after delete → 404.
	rr = httptest.NewRecorder()
	h.h.GetCustomer(rr, withURLParam(h.req(http.MethodGet, "/v1/customers/"+id, "", merchantID), "id", id))
	if rr.Code != http.StatusNotFound {
		t.Errorf("get after delete status = %d, want 404", rr.Code)
	}
}

func TestListCustomersBadCursor(t *testing.T) {
	h := newHarness(t)
	rr := httptest.NewRecorder()
	h.h.ListCustomers(rr, h.req(http.MethodGet, "/v1/customers?cursor=bad", "", "11111111-1111-1111-1111-000000000009"))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for bad cursor", rr.Code)
	}
}
