// Package plaid adapts the Plaid Go SDK to the internal/processor charge and
// payout interfaces, modeling ACH bank transfers (debit = charge, credit =
// payout). Plaid has no concept of subscriptions, so this adapter implements
// processor.ChargePayoutProcessor, not the full Processor.
//
// PCI-DSS: Plaid holds the bank credentials and account access tokens; this
// adapter references only opaque Plaid tokens/ids and never sees account or
// routing numbers.
package plaid

import (
	plaid "github.com/plaid/plaid-go/v31/plaid"

	"github.com/yahya-elkady/ledger/internal/processor"
)

// Client is a mode-aware Plaid adapter. It holds one SDK client per environment
// (sandbox for test mode, production for live), so a test transfer never touches
// the production environment.
type Client struct {
	sandbox    *plaid.APIClient
	production *plaid.APIClient
	policy     processor.RetryPolicy
}

// Config carries the Plaid client id and the per-environment secrets.
type Config struct {
	ClientID         string
	SandboxSecret    string
	ProductionSecret string
}

var _ processor.ChargePayoutProcessor = (*Client)(nil)

// New constructs a Plaid adapter. Either environment may be left unconfigured;
// a call into an unconfigured mode fails fast with an authentication error.
func New(cfg Config) *Client {
	c := &Client{policy: processor.DefaultRetryPolicy}
	if cfg.ClientID != "" && cfg.SandboxSecret != "" {
		c.sandbox = newAPIClient(cfg.ClientID, cfg.SandboxSecret, plaid.Sandbox)
	}
	if cfg.ClientID != "" && cfg.ProductionSecret != "" {
		c.production = newAPIClient(cfg.ClientID, cfg.ProductionSecret, plaid.Production)
	}
	return c
}

// newAPIClient builds a Plaid SDK client bound to one environment + secret.
func newAPIClient(clientID, secret string, env plaid.Environment) *plaid.APIClient {
	cfg := plaid.NewConfiguration()
	cfg.AddDefaultHeader("PLAID-CLIENT-ID", clientID)
	cfg.AddDefaultHeader("PLAID-SECRET", secret)
	cfg.UseEnvironment(env)
	return plaid.NewAPIClient(cfg)
}

// forMode returns the SDK client for the request mode (live => production,
// otherwise sandbox), or an auth error if that environment is unconfigured.
func (c *Client) forMode(mode string) (*plaid.APIClient, error) {
	if mode == "live" {
		if c.production == nil {
			return nil, processor.NewError(processor.CodeAuth, false, nil, "no Plaid production credentials configured")
		}
		return c.production, nil
	}
	if c.sandbox == nil {
		return nil, processor.NewError(processor.CodeAuth, false, nil, "no Plaid sandbox credentials configured")
	}
	return c.sandbox, nil
}
