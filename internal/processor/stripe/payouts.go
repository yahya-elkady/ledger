package stripe

import (
	"context"
	"time"

	stripe "github.com/stripe/stripe-go/v76"

	"github.com/yahya-elkady/ledger/internal/processor"
)

// CreatePayout initiates a Stripe payout to an external account (the merchant's
// connected bank account). Stripe returns the payout in a pending state; the
// terminal status (paid/failed) arrives later via webhook.
//
// PCI-DSS: bank credentials are held by Stripe; only an opaque destination id
// is referenced here.
func (c *Client) CreatePayout(ctx context.Context, req processor.PayoutRequest) (processor.PayoutResult, error) {
	api, err := c.forMode(req.Mode)
	if err != nil {
		return processor.PayoutResult{}, err
	}

	params := &stripe.PayoutParams{
		Amount:   ptrInt64(req.Amount),
		Currency: ptrString(req.Currency),
	}
	if req.ProcessorAcctID != "" {
		params.Destination = ptrString(req.ProcessorAcctID)
	}
	params.Context = ctx

	po, err := processor.Retry(ctx, c.policy, func() (*stripe.Payout, error) {
		p, callErr := api.Payouts.New(params)
		return p, classify(callErr)
	})
	if err != nil {
		return processor.PayoutResult{}, err
	}

	return processor.PayoutResult{
		ProcessorPayoutID: po.ID,
		Status:            string(po.Status),
		ArrivalDate:       time.Unix(po.ArrivalDate, 0).UTC(),
	}, nil
}
