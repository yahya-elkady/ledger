package webhook_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/yahya-elkady/ledger/internal/webhook"
)

// stripeSigHeader builds a valid Stripe-Signature header for payload under
// secret at the given unix timestamp: "t=<ts>,v1=<hex hmac of ts.payload>".
func stripeSigHeader(payload []byte, secret string, ts int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "%d.%s", ts, payload)
	return "t=" + strconv.FormatInt(ts, 10) + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func TestStripeVerifierValidSignature(t *testing.T) {
	const secret = "whsec_test_secret"
	v := webhook.NewStripeVerifier(secret)
	payload := []byte(`{"type":"payment_intent.succeeded","data":{"object":{"id":"pi_123"}}}`)
	header := stripeSigHeader(payload, secret, time.Now().Unix())

	ev, err := v.Verify(payload, header)
	if err != nil {
		t.Fatalf("Verify: unexpected error %v", err)
	}
	if ev.Kind != webhook.ChargeSucceeded {
		t.Errorf("Kind = %v, want ChargeSucceeded", ev.Kind)
	}
	if ev.ObjectID != "pi_123" {
		t.Errorf("ObjectID = %q, want pi_123", ev.ObjectID)
	}
}

func TestStripeVerifierFailedChargeMapsFailure(t *testing.T) {
	const secret = "whsec_test_secret"
	v := webhook.NewStripeVerifier(secret)
	payload := []byte(`{"type":"payment_intent.payment_failed","data":{"object":{"id":"pi_9","last_payment_error":{"code":"card_declined","message":"Your card was declined."}}}}`)
	header := stripeSigHeader(payload, secret, time.Now().Unix())

	ev, err := v.Verify(payload, header)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if ev.Kind != webhook.ChargeFailed || ev.FailureCode != "card_declined" {
		t.Errorf("got kind=%v code=%q, want ChargeFailed/card_declined", ev.Kind, ev.FailureCode)
	}
}

func TestStripeVerifierRejectsTamper(t *testing.T) {
	const secret = "whsec_test_secret"
	v := webhook.NewStripeVerifier(secret)
	payload := []byte(`{"type":"payment_intent.succeeded","data":{"object":{"id":"pi_123"}}}`)
	header := stripeSigHeader(payload, secret, time.Now().Unix())

	// Tamper with the payload after signing — signature must no longer verify.
	tampered := []byte(`{"type":"payment_intent.succeeded","data":{"object":{"id":"pi_EVIL"}}}`)
	if _, err := v.Verify(tampered, header); err != webhook.ErrInvalidSignature {
		t.Errorf("tampered payload: err = %v, want ErrInvalidSignature", err)
	}
}

func TestStripeVerifierEmptySecretFailsClosed(t *testing.T) {
	v := webhook.NewStripeVerifier("")
	if _, err := v.Verify([]byte(`{}`), "t=1,v1=abc"); err != webhook.ErrInvalidSignature {
		t.Errorf("empty secret: err = %v, want ErrInvalidSignature", err)
	}
}

func TestPlaidVerifierFailsClosed(t *testing.T) {
	v := webhook.NewPlaidVerifier()
	if _, err := v.Verify([]byte(`{}`), "header"); err != webhook.ErrInvalidSignature {
		t.Errorf("err = %v, want ErrInvalidSignature", err)
	}
}
