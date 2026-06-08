package payment

import (
	"context"
	"errors"

	"github.com/yahya-elkady/ledger/internal/ledger"
)

// Provider is the external payment processor seen from the payment layer.
//
// It is the seam between our domain and a real processor (Stripe, Adyen, …).
// The payment layer holds money-movement logic; the Provider only confirms,
// captures, and releases holds with the outside world. The real adapter built
// later is simply another implementation of this same interface — no payment
// logic changes.
type Provider interface {
	// Authorize asks the processor to confirm and hold funds for amount.
	// On success it returns a providerRef that Capture and Void later require.
	// A refused authorization is reported as ErrDeclined.
	Authorize(ctx context.Context, amount ledger.Money) (providerRef string, err error)
	// Capture takes funds previously held under providerRef.
	Capture(ctx context.Context, providerRef string) error
	// Void releases a hold under providerRef without taking funds.
	Void(ctx context.Context, providerRef string) error
}

// ErrDeclined is returned by a Provider's Authorize when the processor refuses
// the authorization (e.g. a declined card). It is a normal business outcome,
// distinct from a transport/processor error: the orchestration maps a decline
// to a failed payment, whereas an unexpected error is surfaced to the caller.
var ErrDeclined = errors.New("provider declined authorization")
