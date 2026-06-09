package stripe

import (
	"context"
	"errors"

	stripe "github.com/stripe/stripe-go/v76"

	"github.com/yahya-elkady/ledger/internal/processor"
)

// CreateCharge creates and confirms a Stripe PaymentIntent. A card decline is a
// *successful* call returning a "failed" result (not an error); operational
// failures (5xx, throttling, network) are retried and then surfaced as errors.
//
// PCI-DSS: only a tokenized payment-method id is sent; no PAN/CVV is handled here.
func (c *Client) CreateCharge(ctx context.Context, req processor.ChargeRequest) (processor.ChargeResult, error) {
	api, err := c.forMode(req.Mode)
	if err != nil {
		return processor.ChargeResult{}, err
	}

	params := &stripe.PaymentIntentParams{
		Amount:        ptrInt64(req.Amount),
		Currency:      ptrString(req.Currency),
		PaymentMethod: ptrString(req.ProcessorMethodID),
		Confirm:       stripe.Bool(true),
	}
	if req.Description != "" {
		params.Description = ptrString(req.Description)
	}
	params.Context = ctx

	pi, err := processor.Retry(ctx, c.policy, func() (*stripe.PaymentIntent, error) {
		pi, callErr := api.PaymentIntents.New(params)
		return pi, classify(callErr)
	})
	if err != nil {
		// A card decline is reported by Stripe as an error; translate it into a
		// failed (but non-erroring) result so the charge is still recorded.
		if decl, ok := asDecline(err); ok {
			return processor.ChargeResult{
				Status:         "failed",
				FailureCode:    decl.code,
				FailureMessage: decl.message,
			}, nil
		}
		return processor.ChargeResult{}, err
	}

	return processor.ChargeResult{
		ProcessorChargeID: pi.ID,
		Status:            chargeStatus(pi.Status),
	}, nil
}

// RefundCharge refunds (fully or partially) a PaymentIntent.
//
// PCI-DSS: operates on a stored Stripe reference id; no card data is involved.
func (c *Client) RefundCharge(ctx context.Context, _ /*processorName*/, processorChargeID string, amount int64, mode string) (processor.RefundResult, error) {
	api, err := c.forMode(mode)
	if err != nil {
		return processor.RefundResult{}, err
	}

	params := &stripe.RefundParams{PaymentIntent: ptrString(processorChargeID)}
	if amount > 0 {
		params.Amount = ptrInt64(amount)
	}
	params.Context = ctx

	re, err := processor.Retry(ctx, c.policy, func() (*stripe.Refund, error) {
		re, callErr := api.Refunds.New(params)
		return re, classify(callErr)
	})
	if err != nil {
		return processor.RefundResult{}, err
	}
	return processor.RefundResult{ProcessorRefundID: re.ID, Status: string(re.Status)}, nil
}

// chargeStatus maps a Stripe PaymentIntent status to our succeeded/failed model.
func chargeStatus(s stripe.PaymentIntentStatus) string {
	if s == stripe.PaymentIntentStatusSucceeded {
		return "succeeded"
	}
	// requires_action / processing / etc. are not "succeeded" yet; the webhook
	// will advance them. Treat anything non-succeeded at creation as failed for
	// the synchronous response.
	return "failed"
}

// decline captures the merchant-facing detail of a declined card.
type decline struct {
	code    string
	message string
}

// asDecline reports whether err is a Stripe card decline and extracts its code.
func asDecline(err error) (decline, bool) {
	var se *stripe.Error
	if errors.As(err, &se) && se.Type == stripe.ErrorTypeCard {
		code := string(se.Code)
		if se.DeclineCode != "" {
			code = string(se.DeclineCode)
		}
		return decline{code: code, message: se.Msg}, true
	}
	return decline{}, false
}

// classify turns a raw Stripe SDK error into a normalized, retry-flagged
// processor.Error. Card declines are handled separately (asDecline) and never
// reach here as errors to surface.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var se *stripe.Error
	if !errors.As(err, &se) {
		// Non-Stripe error (network/transport): treat as transient.
		return processor.NewError(processor.CodeUnavailable, true, err, "stripe transport error")
	}
	switch {
	case se.HTTPStatusCode == 429:
		return processor.NewError(processor.CodeRateLimited, true, err, "stripe rate limited")
	case se.HTTPStatusCode >= 500:
		return processor.NewError(processor.CodeUnavailable, true, err, "stripe server error")
	case se.HTTPStatusCode == 401:
		return processor.NewError(processor.CodeAuth, false, err, "stripe authentication failed")
	case se.Type == stripe.ErrorTypeInvalidRequest:
		return processor.NewError(processor.CodeInvalidRequest, false, err, "stripe rejected request: %s", se.Msg)
	default:
		return processor.NewError(processor.CodeUnknown, false, err, "stripe error: %s", se.Msg)
	}
}
