package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yahya-elkady/ledger/internal/api/middleware"
)

// reqMode is like harness.req but mints the access token with an explicit mode,
// then runs it through the real JWTMiddleware so the handler sees exactly the
// mode the middleware would inject into context.
func (h *harness) reqMode(method, target, body, merchantID, mode string) *http.Request {
	tok, err := h.jwt.IssueAccessToken(merchantID, mode, []string{"admin"})
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

// TestModeIsolationCharges proves a test-mode principal cannot read a charge
// created in live mode, and vice versa — the core test/live isolation guarantee
// (Phase 9). The fake store mirrors the production queries, which filter every
// get/list by mode.
func TestModeIsolationCharges(t *testing.T) {
	h := newHarness(t)

	// Create a charge as a LIVE-mode principal.
	rr := httptest.NewRecorder()
	h.h.CreateCharge(rr, h.reqMode(http.MethodPost, "/v1/charges",
		`{"amount":4242,"currency":"USD","payment_method_id":"pm","processor":"stripe"}`, chargeMerchant, "live"))
	if rr.Code != http.StatusCreated {
		t.Fatalf("live charge create status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	liveChargeID := decode[map[string]any](t, rr)["id"].(string)

	// A TEST-mode principal must NOT be able to GET the live charge → 404.
	rr = httptest.NewRecorder()
	h.h.GetCharge(rr, withURLParam(
		h.reqMode(http.MethodGet, "/v1/charges/"+liveChargeID, "", chargeMerchant, "test"),
		"id", liveChargeID))
	if rr.Code != http.StatusNotFound {
		t.Errorf("test-mode GET of live charge: status = %d, want 404", rr.Code)
	}

	// The same charge IS visible to a LIVE-mode principal → 200.
	rr = httptest.NewRecorder()
	h.h.GetCharge(rr, withURLParam(
		h.reqMode(http.MethodGet, "/v1/charges/"+liveChargeID, "", chargeMerchant, "live"),
		"id", liveChargeID))
	if rr.Code != http.StatusOK {
		t.Errorf("live-mode GET of live charge: status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	// Listing in test mode returns none of the merchant's live charges.
	rr = httptest.NewRecorder()
	h.h.ListCharges(rr, h.reqMode(http.MethodGet, "/v1/charges", "", chargeMerchant, "test"))
	if rr.Code != http.StatusOK {
		t.Fatalf("test-mode list status = %d, want 200", rr.Code)
	}
	if data := decode[map[string]any](t, rr)["data"]; data != nil {
		if items, ok := data.([]any); ok && len(items) != 0 {
			t.Errorf("test-mode list returned %d charges, want 0 (live charge leaked)", len(items))
		}
	}

	// Listing in live mode returns the live charge.
	rr = httptest.NewRecorder()
	h.h.ListCharges(rr, h.reqMode(http.MethodGet, "/v1/charges", "", chargeMerchant, "live"))
	items, ok := decode[map[string]any](t, rr)["data"].([]any)
	if !ok || len(items) != 1 {
		t.Errorf("live-mode list returned %v, want exactly 1 charge", decode[map[string]any](t, rr)["data"])
	}
}
