package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// DefaultToleranceSeconds is the default replay-protection window for
// VerifySignature: a signature whose timestamp is more than this many seconds
// from "now" is rejected even if the HMAC matches (build.md Phase 7).
const DefaultToleranceSeconds = 300

// SignPayload computes the outbound webhook signature for a payload:
// hex(HMAC-SHA256(secret, "<timestamp>.<payload>")). Binding the timestamp
// into the signed message is what makes replay protection possible — an
// attacker cannot reuse an old signature with a fresh timestamp.
func SignPayload(payload []byte, secret string, timestamp int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", timestamp)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature checks an outbound-webhook signature the way a merchant's
// endpoint should: recompute the HMAC over "<timestamp>.<payload>" and compare
// in constant time, then reject timestamps outside the tolerance window
// (|now - timestamp| <= toleranceSeconds) to block replays of captured
// requests. toleranceSeconds <= 0 falls back to DefaultToleranceSeconds.
func VerifySignature(payload []byte, signature, secret string, timestamp int64, toleranceSeconds int) bool {
	if toleranceSeconds <= 0 {
		toleranceSeconds = DefaultToleranceSeconds
	}
	skew := time.Now().Unix() - timestamp
	if skew < 0 {
		skew = -skew
	}
	if skew > int64(toleranceSeconds) {
		return false
	}
	expected := SignPayload(payload, secret, timestamp)
	// hmac.Equal: constant-time compare, same rule as API-key validation.
	return hmac.Equal([]byte(expected), []byte(signature))
}

// DeriveEndpointSecret derives a per-endpoint signing secret from the master
// WEBHOOK_SIGNING_SECRET: hex(HMAC-SHA256(master, "webhook-endpoint:<id>")).
//
// The derived secret is deterministic, so it is never stored — the endpoint
// registration flow shows it to the merchant once, and the dispatcher
// recomputes it at delivery time. A compromise of one endpoint's secret does
// not reveal the master secret or any other endpoint's secret.
func DeriveEndpointSecret(masterSecret, endpointID string) string {
	mac := hmac.New(sha256.New, []byte(masterSecret))
	mac.Write([]byte("webhook-endpoint:" + endpointID))
	return hex.EncodeToString(mac.Sum(nil))
}
