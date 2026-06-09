// Package webhook handles inbound webhooks from payment processors. Signature
// verification and event parsing are processor-specific (Stripe/Plaid SDKs,
// Phase 6); this package defines the seam — a Verifier that turns a raw signed
// payload into a normalized Event — plus a controllable fake for tests.
package webhook

import "errors"

// ErrInvalidSignature is returned when a webhook payload's signature does not
// verify. The handler maps it to a 4xx; an unsigned/forged event is never
// processed (build.md webhook security rule).
var ErrInvalidSignature = errors.New("invalid webhook signature")

// Kind classifies a normalized inbound event. Unhandled events are acknowledged
// (200) but cause no state change.
type Kind int

const (
	Unhandled Kind = iota
	ChargeSucceeded
	ChargeFailed
	PayoutPaid
	PayoutFailed
	SubscriptionUpdated
	SubscriptionCanceled
)

// Event is a processor-agnostic view of an inbound webhook, carrying just what
// the dispatcher needs to advance the matching record's status.
type Event struct {
	Kind           Kind
	ObjectID       string // processor id of the affected charge/payout/subscription
	Status         string // for SubscriptionUpdated: the new status
	FailureCode    string // for ChargeFailed
	FailureMessage string // for ChargeFailed / PayoutFailed
}

// Verifier authenticates a raw webhook payload and parses it into an Event.
// Implementations verify the processor's signature header before trusting any
// of the payload's contents.
type Verifier interface {
	Verify(payload []byte, signatureHeader string) (Event, error)
}

// Fake is a controllable Verifier for tests: it returns the preset Event, or
// Err (e.g. ErrInvalidSignature) to exercise the rejection path.
type Fake struct {
	Event Event
	Err   error
}

// Verify returns the fake's configured event or error.
func (f *Fake) Verify(_ []byte, _ string) (Event, error) {
	return f.Event, f.Err
}
