// Package stripe adapts the Stripe Go SDK to the internal/processor interfaces.
// It is the real implementation behind processor.ChargeProcessor,
// SubscriptionProcessor, and PayoutProcessor.
//
// PCI-DSS: Stripe handles raw card data; this adapter never receives, stores, or
// logs card numbers or CVVs — only tokenized payment-method ids and Stripe
// object references cross this boundary.
package stripe

import (
	stripe "github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/client"

	"github.com/yahya-elkady/ledger/internal/processor"
)

// Client is a mode-aware Stripe adapter. It holds two underlying SDK clients —
// one keyed for live, one for test — and selects between them per request, so a
// live key is never used to service a test-mode call (and vice versa).
type Client struct {
	live   *client.API
	test   *client.API
	policy processor.RetryPolicy
}

// Config carries the two secret keys (live + test) for the Stripe account.
type Config struct {
	LiveKey string
	TestKey string
}

// New constructs a Stripe adapter from the configured keys. Either key may be
// empty in environments that only use one mode; a call into an unconfigured
// mode fails fast with an authentication error.
func New(cfg Config) *Client {
	c := &Client{policy: processor.DefaultRetryPolicy}
	if cfg.LiveKey != "" {
		c.live = newAPI(cfg.LiveKey)
	}
	if cfg.TestKey != "" {
		c.test = newAPI(cfg.TestKey)
	}
	return c
}

// newAPI builds a Stripe SDK client bound to one key.
func newAPI(key string) *client.API {
	api := &client.API{}
	api.Init(key, nil)
	return api
}

// forMode returns the SDK client for the request mode, or an auth error if that
// mode has no configured key.
func (c *Client) forMode(mode string) (*client.API, error) {
	switch mode {
	case "live":
		if c.live == nil {
			return nil, authError("live")
		}
		return c.live, nil
	default: // "test" (and any unspecified mode) maps to the test key
		if c.test == nil {
			return nil, authError("test")
		}
		return c.test, nil
	}
}

func authError(mode string) error {
	return processor.NewError(processor.CodeAuth, false, nil, "no Stripe key configured for %s mode", mode)
}

// compile-time checks that Client satisfies the processor interfaces.
var (
	_ processor.ChargeProcessor       = (*Client)(nil)
	_ processor.SubscriptionProcessor = (*Client)(nil)
	_ processor.PayoutProcessor       = (*Client)(nil)
)

// ptrString / ptrInt64 are local convenience wrappers around the SDK helpers.
func ptrString(s string) *string { return stripe.String(s) }
func ptrInt64(i int64) *int64    { return stripe.Int64(i) }
