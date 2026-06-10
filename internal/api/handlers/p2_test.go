package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yahya-elkady/ledger/internal/models"
	"github.com/yahya-elkady/ledger/internal/store"
	"github.com/yahya-elkady/ledger/internal/webhook"
)

const p2Merchant = "11111111-1111-1111-1111-000000000077"

func TestPlanAndSubscriptionFlow(t *testing.T) {
	h := newHarness(t)

	// Create a plan.
	rr := httptest.NewRecorder()
	h.h.CreatePlan(rr, h.req(http.MethodPost, "/v1/plans",
		`{"name":"Pro","amount":2900,"currency":"USD","interval":"month"}`, p2Merchant))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create plan status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	planID := decode[map[string]any](t, rr)["id"].(string)

	// Subscribe a customer to it.
	rr = httptest.NewRecorder()
	body := `{"customer_id":"cust-1","plan_id":"` + planID + `","payment_method_id":"pm_1"}`
	h.h.CreateSubscription(rr, h.req(http.MethodPost, "/v1/subscriptions", body, p2Merchant))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create subscription status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	sub := decode[map[string]any](t, rr)
	subID := sub["id"].(string)
	if sub["status"] != "active" {
		t.Errorf("subscription status = %v, want active", sub["status"])
	}

	// Cancel immediately.
	rr = httptest.NewRecorder()
	h.h.CancelSubscription(rr, withURLParam(h.req(http.MethodPost, "/v1/subscriptions/"+subID+"/cancel", `{"at_period_end":false}`, p2Merchant), "id", subID))
	if rr.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := decode[map[string]any](t, rr); got["status"] != "canceled" {
		t.Errorf("status = %v, want canceled", got["status"])
	}
}

func TestSubscriptionRequiresKnownPlan(t *testing.T) {
	h := newHarness(t)
	rr := httptest.NewRecorder()
	body := `{"customer_id":"c","plan_id":"99999999-9999-9999-9999-999999999999","payment_method_id":"pm"}`
	h.h.CreateSubscription(rr, h.req(http.MethodPost, "/v1/subscriptions", body, p2Merchant))
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for unknown plan", rr.Code)
	}
}

func TestBankAccountAndPayoutFlow(t *testing.T) {
	h := newHarness(t)

	// Register a bank account.
	rr := httptest.NewRecorder()
	h.h.CreateBankAccount(rr, h.req(http.MethodPost, "/v1/bank-accounts",
		`{"processor":"stripe","processor_acct_id":"ba_tok","last4":"6789","bank_name":"Acme"}`, p2Merchant))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create bank account status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	bankID := decode[map[string]any](t, rr)["id"].(string)

	// Initiate a payout to it.
	rr = httptest.NewRecorder()
	body := `{"amount":5000,"currency":"USD","bank_account_id":"` + bankID + `"}`
	h.h.CreatePayout(rr, h.req(http.MethodPost, "/v1/payouts", body, p2Merchant))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create payout status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	po := decode[map[string]any](t, rr)
	if po["status"] != "pending" {
		t.Errorf("payout status = %v, want pending", po["status"])
	}
	if h.processor.PayoutCalls != 1 {
		t.Errorf("processor CreatePayout calls = %d, want 1", h.processor.PayoutCalls)
	}
}

func TestPayoutUnknownBankAccount(t *testing.T) {
	h := newHarness(t)
	rr := httptest.NewRecorder()
	body := `{"amount":5000,"currency":"USD","bank_account_id":"99999999-9999-9999-9999-999999999999"}`
	h.h.CreatePayout(rr, h.req(http.MethodPost, "/v1/payouts", body, p2Merchant))
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for unknown bank account", rr.Code)
	}
}

func TestStripeWebhookUpdatesCharge(t *testing.T) {
	h := newHarness(t)

	// Seed a pending charge with a known processor id.
	h.charges.byID["seed"] = &models.Charge{
		ID: "seed", MerchantID: p2Merchant, Mode: "test", Status: "pending", ProcessorChargeID: "ch_777",
	}

	// A verified payment_intent.succeeded advances it to succeeded.
	h.stripeHook.Event = webhook.Event{Kind: webhook.ChargeSucceeded, ObjectID: "ch_777"}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/stripe", nil)
	req.Header.Set("Stripe-Signature", "sig")
	h.h.StripeWebhook(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("webhook status = %d, want 200", rr.Code)
	}
	if h.charges.byID["seed"].Status != "succeeded" {
		t.Errorf("charge status = %q, want succeeded after webhook", h.charges.byID["seed"].Status)
	}

	// The inbound event is relayed to the merchant's own webhook endpoints
	// (Phase 7): one outbound charge.succeeded queued for the owning merchant.
	if len(h.events.events) != 1 {
		t.Fatalf("outbound events = %d, want 1", len(h.events.events))
	}
	ev := h.events.events[0]
	if ev.Type != "charge.succeeded" || ev.MerchantID != p2Merchant || ev.Mode != "test" {
		t.Errorf("relayed event = %+v, want charge.succeeded for %s/test", ev, p2Merchant)
	}
}

func TestCreateChargeEmitsOutboundEvent(t *testing.T) {
	h := newHarness(t)
	rr := httptest.NewRecorder()
	body := `{"amount":1000,"currency":"USD","payment_method_id":"pm_1","processor":"stripe"}`
	req := h.req(http.MethodPost, "/v1/charges", body, p2Merchant)
	req.Header.Set("Idempotency-Key", "idem-emit-1")
	h.h.CreateCharge(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create charge status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	if len(h.events.events) != 1 || h.events.events[0].Type != "charge.succeeded" {
		t.Fatalf("events = %+v, want one charge.succeeded", h.events.events)
	}
}

func TestStripeWebhookRejectsBadSignature(t *testing.T) {
	h := newHarness(t)
	h.stripeHook.Err = webhook.ErrInvalidSignature
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/stripe", nil)
	req.Header.Set("Stripe-Signature", "bad")
	h.h.StripeWebhook(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid signature", rr.Code)
	}
}

func TestDashboardOverview(t *testing.T) {
	h := newHarness(t)
	h.dashboard.stats = store.ChargeStats{TotalCount: 10, SucceededCount: 8, SucceededVolume: 12345, FailedCount: 2}
	h.dashboard.activeSubs = 3
	h.dashboard.pendingPayouts = 1

	rr := httptest.NewRecorder()
	h.h.DashboardOverview(rr, h.req(http.MethodGet, "/v1/dashboard/overview", "", p2Merchant))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	got := decode[map[string]any](t, rr)
	if got["gross_volume"].(float64) != 12345 {
		t.Errorf("gross_volume = %v, want 12345", got["gross_volume"])
	}
	if got["active_subscriptions"].(float64) != 3 {
		t.Errorf("active_subscriptions = %v, want 3", got["active_subscriptions"])
	}
	if got["mode"] != "test" {
		t.Errorf("mode = %v, want test", got["mode"])
	}
}
