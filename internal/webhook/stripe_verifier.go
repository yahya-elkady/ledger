package webhook

import (
	stripe "github.com/stripe/stripe-go/v76"
	stripewebhook "github.com/stripe/stripe-go/v76/webhook"
)

// StripeVerifier authenticates inbound Stripe webhooks using the endpoint's
// signing secret (STRIPE_WEBHOOK_SECRET) and normalizes the event into an Event.
//
// Webhook security: it calls stripe.ConstructEvent, which verifies the
// `Stripe-Signature` HMAC and the timestamp tolerance before the payload is
// trusted. A missing/forged signature yields ErrInvalidSignature, so a forged
// event is never processed.
type StripeVerifier struct {
	signingSecret string
}

// NewStripeVerifier constructs a verifier for the given signing secret. An empty
// secret makes Verify fail closed (every event is rejected), so the service
// never processes unverified Stripe events when the secret is unconfigured.
func NewStripeVerifier(signingSecret string) *StripeVerifier {
	return &StripeVerifier{signingSecret: signingSecret}
}

// Verify checks the Stripe-Signature header against the raw payload and maps the
// parsed event to a normalized Event. Unrecognized event types map to Unhandled
// (acknowledged, no state change).
func (v *StripeVerifier) Verify(payload []byte, signatureHeader string) (Event, error) {
	if v.signingSecret == "" {
		return Event{}, ErrInvalidSignature
	}
	// Ignore the stripe-go API-version check: we read the raw object map rather
	// than typed structs, so a mismatch between the merchant's account version
	// and the SDK's pinned version is irrelevant — the signature + timestamp
	// tolerance are what gate trust.
	event, err := stripewebhook.ConstructEventWithOptions(payload, signatureHeader, v.signingSecret,
		stripewebhook.ConstructEventOptions{IgnoreAPIVersionMismatch: true})
	if err != nil {
		return Event{}, ErrInvalidSignature
	}
	return stripeEventToEvent(event), nil
}

// stripeEventToEvent translates a verified stripe.Event into the package's
// processor-agnostic Event. Object id and failure details are read from the
// event's raw object map.
func stripeEventToEvent(e stripe.Event) Event {
	obj := e.Data.Object
	id, _ := obj["id"].(string)

	switch e.Type {
	case stripe.EventTypePaymentIntentSucceeded:
		return Event{Kind: ChargeSucceeded, ObjectID: id}
	case stripe.EventTypePaymentIntentPaymentFailed:
		code, msg := stripeLastPaymentError(obj)
		return Event{Kind: ChargeFailed, ObjectID: id, FailureCode: code, FailureMessage: msg}
	case stripe.EventTypePayoutPaid:
		return Event{Kind: PayoutPaid, ObjectID: id}
	case stripe.EventTypePayoutFailed:
		msg, _ := obj["failure_message"].(string)
		return Event{Kind: PayoutFailed, ObjectID: id, FailureMessage: msg}
	case stripe.EventTypeCustomerSubscriptionDeleted:
		return Event{Kind: SubscriptionCanceled, ObjectID: id}
	case stripe.EventTypeCustomerSubscriptionUpdated:
		status, _ := obj["status"].(string)
		return Event{Kind: SubscriptionUpdated, ObjectID: id, Status: status}
	default:
		return Event{Kind: Unhandled, ObjectID: id}
	}
}

// stripeLastPaymentError pulls the decline code/message from a PaymentIntent's
// nested last_payment_error object, if present.
func stripeLastPaymentError(obj map[string]interface{}) (code, message string) {
	lpe, ok := obj["last_payment_error"].(map[string]interface{})
	if !ok {
		return "", ""
	}
	code, _ = lpe["code"].(string)
	message, _ = lpe["message"].(string)
	return code, message
}
