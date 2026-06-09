package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const chargeMerchant = "11111111-1111-1111-1111-000000000042"

func TestCreateChargeSucceeds(t *testing.T) {
	h := newHarness(t)
	rr := httptest.NewRecorder()
	req := h.req(http.MethodPost, "/v1/charges",
		`{"amount":1999,"currency":"USD","payment_method_id":"pm_tok","processor":"stripe"}`, chargeMerchant)
	req.Header.Set("Idempotency-Key", "idem-charge-1")
	h.h.CreateCharge(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	got := decode[map[string]any](t, rr)
	if got["status"] != "succeeded" {
		t.Errorf("status = %v, want succeeded", got["status"])
	}
	if got["amount"].(float64) != 1999 {
		t.Errorf("amount = %v, want 1999", got["amount"])
	}
	if h.processor.ChargeCalls != 1 {
		t.Errorf("processor CreateCharge calls = %d, want 1", h.processor.ChargeCalls)
	}
}

func TestCreateChargeDeclined(t *testing.T) {
	h := newHarness(t)
	h.processor.DeclineCharge = true
	rr := httptest.NewRecorder()
	req := h.req(http.MethodPost, "/v1/charges",
		`{"amount":500,"currency":"USD","payment_method_id":"pm_tok","processor":"stripe"}`, chargeMerchant)
	h.h.CreateCharge(rr, req)

	// A decline is still a persisted charge (status failed), returned 201.
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	if got := decode[map[string]any](t, rr); got["status"] != "failed" {
		t.Errorf("status = %v, want failed", got["status"])
	}
}

func TestCreateChargeValidation(t *testing.T) {
	h := newHarness(t)
	cases := map[string]string{
		"non-positive amount": `{"amount":0,"currency":"USD","payment_method_id":"pm","processor":"stripe"}`,
		"bad currency":        `{"amount":100,"currency":"ZZZ","payment_method_id":"pm","processor":"stripe"}`,
		"no target":           `{"amount":100,"currency":"USD","processor":"stripe"}`,
		"bad processor":       `{"amount":100,"currency":"USD","payment_method_id":"pm","processor":"acme"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.h.CreateCharge(rr, h.req(http.MethodPost, "/v1/charges", body, chargeMerchant))
			if rr.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rr.Code)
			}
		})
	}
}

func TestChargeIdempotencyConflict(t *testing.T) {
	h := newHarness(t)
	mk := func() *http.Request {
		req := h.req(http.MethodPost, "/v1/charges",
			`{"amount":1000,"currency":"USD","payment_method_id":"pm","processor":"stripe"}`, chargeMerchant)
		req.Header.Set("Idempotency-Key", "dup-key")
		return req
	}
	rr := httptest.NewRecorder()
	h.h.CreateCharge(rr, mk())
	if rr.Code != http.StatusCreated {
		t.Fatalf("first charge status = %d, want 201", rr.Code)
	}

	rr = httptest.NewRecorder()
	h.h.CreateCharge(rr, mk())
	if rr.Code != http.StatusConflict {
		t.Errorf("duplicate idempotency key status = %d, want 409", rr.Code)
	}
}

func TestRefundCharge(t *testing.T) {
	h := newHarness(t)

	// Create a succeeded charge.
	rr := httptest.NewRecorder()
	req := h.req(http.MethodPost, "/v1/charges",
		`{"amount":1000,"currency":"USD","payment_method_id":"pm","processor":"stripe"}`, chargeMerchant)
	h.h.CreateCharge(rr, req)
	chargeID := decode[map[string]any](t, rr)["id"].(string)

	// Partial refund.
	rr = httptest.NewRecorder()
	h.h.RefundCharge(rr, withURLParam(h.req(http.MethodPost, "/v1/charges/"+chargeID+"/refund", `{"amount":400}`, chargeMerchant), "id", chargeID))
	if rr.Code != http.StatusOK {
		t.Fatalf("partial refund status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := decode[map[string]any](t, rr); got["status"] != "partially_refunded" {
		t.Errorf("status = %v, want partially_refunded", got["status"])
	}

	// Over-refund the remainder + extra → 400.
	rr = httptest.NewRecorder()
	h.h.RefundCharge(rr, withURLParam(h.req(http.MethodPost, "/v1/charges/"+chargeID+"/refund", `{"amount":9999}`, chargeMerchant), "id", chargeID))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("over-refund status = %d, want 400", rr.Code)
	}

	// Refund the rest → fully refunded.
	rr = httptest.NewRecorder()
	h.h.RefundCharge(rr, withURLParam(h.req(http.MethodPost, "/v1/charges/"+chargeID+"/refund", `{"amount":600}`, chargeMerchant), "id", chargeID))
	if got := decode[map[string]any](t, rr); got["status"] != "refunded" {
		t.Errorf("status = %v, want refunded", got["status"])
	}
}

func TestCreateChargeIsAudited(t *testing.T) {
	h := newHarness(t)
	rr := httptest.NewRecorder()
	h.h.CreateCharge(rr, h.req(http.MethodPost, "/v1/charges",
		`{"amount":100,"currency":"USD","payment_method_id":"pm","processor":"stripe"}`, chargeMerchant))
	if !strings.Contains(strings.Join(h.audit.actions(), ","), "charge.created") {
		t.Error("charge.created audit entry missing")
	}
}
