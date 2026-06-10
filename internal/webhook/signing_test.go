package webhook

import (
	"testing"
	"time"
)

func TestSignAndVerifyRoundTrip(t *testing.T) {
	payload := []byte(`{"id":"evt_1","type":"charge.succeeded"}`)
	secret := "whsec_test_secret_at_least_32_chars!!"
	ts := time.Now().Unix()

	sig := SignPayload(payload, secret, ts)
	if !VerifySignature(payload, sig, secret, ts, 300) {
		t.Error("freshly-signed payload should verify")
	}
}

func TestVerifyRejectsTamperedPayload(t *testing.T) {
	secret := "whsec_test_secret_at_least_32_chars!!"
	ts := time.Now().Unix()
	sig := SignPayload([]byte(`{"amount":100}`), secret, ts)

	if VerifySignature([]byte(`{"amount":999}`), sig, secret, ts, 300) {
		t.Error("tampered payload must not verify")
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	payload := []byte(`{}`)
	ts := time.Now().Unix()
	sig := SignPayload(payload, "secret-a", ts)

	if VerifySignature(payload, sig, "secret-b", ts, 300) {
		t.Error("signature from another secret must not verify")
	}
}

func TestVerifyRejectsStaleTimestamp(t *testing.T) {
	// Replay protection: a valid signature with a timestamp outside the
	// tolerance window is rejected.
	payload := []byte(`{}`)
	secret := "whsec_test_secret_at_least_32_chars!!"
	stale := time.Now().Add(-10 * time.Minute).Unix()
	sig := SignPayload(payload, secret, stale)

	if VerifySignature(payload, sig, secret, stale, 300) {
		t.Error("stale timestamp must be rejected even with a valid HMAC")
	}
	// A future timestamp beyond tolerance is equally invalid.
	future := time.Now().Add(10 * time.Minute).Unix()
	sig = SignPayload(payload, secret, future)
	if VerifySignature(payload, sig, secret, future, 300) {
		t.Error("far-future timestamp must be rejected")
	}
}

func TestVerifyDefaultTolerance(t *testing.T) {
	payload := []byte(`{}`)
	secret := "whsec_test_secret_at_least_32_chars!!"
	ts := time.Now().Unix()
	sig := SignPayload(payload, secret, ts)

	// toleranceSeconds <= 0 falls back to the 300s default.
	if !VerifySignature(payload, sig, secret, ts, 0) {
		t.Error("zero tolerance should fall back to the default window")
	}
}

func TestDeriveEndpointSecret(t *testing.T) {
	master := "master_webhook_secret_32_chars_long!!"

	a1 := DeriveEndpointSecret(master, "ep_a")
	a2 := DeriveEndpointSecret(master, "ep_a")
	b := DeriveEndpointSecret(master, "ep_b")

	if a1 != a2 {
		t.Error("derivation must be deterministic")
	}
	if a1 == b {
		t.Error("different endpoints must get different secrets")
	}
	if a1 == master {
		t.Error("derived secret must not equal the master secret")
	}
}
