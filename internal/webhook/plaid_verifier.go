package webhook

// PlaidVerifier authenticates inbound Plaid webhooks.
//
// Plaid signs each webhook with a JWT in the `Plaid-Verification` header, signed
// ES256 with a key whose public half is fetched from Plaid's
// `/webhook_verification_key/get` endpoint (and rotated). Verifying it requires
// the JWKS fetch + JWT validation + a SHA-256 body hash comparison against the
// JWT's `request_body_sha256` claim.
//
// That full flow needs a live Plaid client and is tracked alongside the other
// Plaid integration gaps (see internal/processor/plaid). Until it lands, this
// verifier FAILS CLOSED: every inbound Plaid webhook is rejected rather than
// processed unverified, so the service never acts on a forged event. Wiring it
// keeps the route present and the security posture correct.
type PlaidVerifier struct{}

// NewPlaidVerifier constructs the fail-closed Plaid verifier.
func NewPlaidVerifier() *PlaidVerifier { return &PlaidVerifier{} }

// Verify currently rejects every event (fail closed) until JWT/JWKS verification
// is implemented. See the type doc for the required flow.
func (v *PlaidVerifier) Verify(_ []byte, _ string) (Event, error) {
	return Event{}, ErrInvalidSignature
}
