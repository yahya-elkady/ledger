package stripe

import (
	"context"
	"time"

	stripe "github.com/stripe/stripe-go/v76"

	"github.com/yahya-elkady/ledger/internal/processor"
)

// CreatePlan creates a Stripe Product and a recurring Price for it, returning
// the Price id (our "processor plan id"). A plan is a (product, price) pair.
func (c *Client) CreatePlan(ctx context.Context, req processor.PlanRequest) (string, error) {
	api, err := c.forMode(req.Mode)
	if err != nil {
		return "", err
	}

	productParams := &stripe.ProductParams{Name: ptrString(req.Name)}
	productParams.Context = ctx
	product, err := processor.Retry(ctx, c.policy, func() (*stripe.Product, error) {
		p, callErr := api.Products.New(productParams)
		return p, classify(callErr)
	})
	if err != nil {
		return "", err
	}

	count := int64(req.IntervalCount)
	if count < 1 {
		count = 1
	}
	priceParams := &stripe.PriceParams{
		Product:    ptrString(product.ID),
		Currency:   ptrString(req.Currency),
		UnitAmount: ptrInt64(req.Amount),
		Recurring: &stripe.PriceRecurringParams{
			Interval:      ptrString(req.Interval),
			IntervalCount: ptrInt64(count),
		},
	}
	priceParams.Context = ctx
	price, err := processor.Retry(ctx, c.policy, func() (*stripe.Price, error) {
		p, callErr := api.Prices.New(priceParams)
		return p, classify(callErr)
	})
	if err != nil {
		return "", err
	}
	return price.ID, nil
}

// CreateSubscription starts a Stripe Subscription on a price.
//
// Note: Stripe subscriptions require a Stripe Customer with an attached payment
// method. The customer/payment-method provisioning flow is not yet modeled by
// this service; ProcessorMethodID is forwarded as the default payment method,
// and a customer id would be threaded here once that flow exists.
func (c *Client) CreateSubscription(ctx context.Context, req processor.SubscriptionRequest) (processor.SubscriptionResult, error) {
	api, err := c.forMode(req.Mode)
	if err != nil {
		return processor.SubscriptionResult{}, err
	}

	params := &stripe.SubscriptionParams{
		Items: []*stripe.SubscriptionItemsParams{{Price: ptrString(req.ProcessorPlanID)}},
	}
	if req.ProcessorMethodID != "" {
		params.DefaultPaymentMethod = ptrString(req.ProcessorMethodID)
	}
	if !req.TrialEnd.IsZero() {
		params.TrialEnd = ptrInt64(req.TrialEnd.Unix())
	}
	params.Context = ctx

	sub, err := processor.Retry(ctx, c.policy, func() (*stripe.Subscription, error) {
		s, callErr := api.Subscriptions.New(params)
		return s, classify(callErr)
	})
	if err != nil {
		return processor.SubscriptionResult{}, err
	}

	return processor.SubscriptionResult{
		ProcessorSubID:     sub.ID,
		Status:             string(sub.Status),
		CurrentPeriodStart: time.Unix(sub.CurrentPeriodStart, 0).UTC(),
		CurrentPeriodEnd:   time.Unix(sub.CurrentPeriodEnd, 0).UTC(),
	}, nil
}

// CancelSubscription cancels a subscription either immediately or at period end.
func (c *Client) CancelSubscription(ctx context.Context, processorSubID string, atPeriodEnd bool, mode string) error {
	api, err := c.forMode(mode)
	if err != nil {
		return err
	}

	if atPeriodEnd {
		// At-period-end is an update (cancel_at_period_end=true), not a delete.
		params := &stripe.SubscriptionParams{CancelAtPeriodEnd: stripe.Bool(true)}
		params.Context = ctx
		_, err = processor.Retry(ctx, c.policy, func() (*stripe.Subscription, error) {
			s, callErr := api.Subscriptions.Update(processorSubID, params)
			return s, classify(callErr)
		})
		return err
	}

	params := &stripe.SubscriptionCancelParams{}
	params.Context = ctx
	_, err = processor.Retry(ctx, c.policy, func() (*stripe.Subscription, error) {
		s, callErr := api.Subscriptions.Cancel(processorSubID, params)
		return s, classify(callErr)
	})
	return err
}

// UpdateSubscription switches a subscription to a new price (plan change).
func (c *Client) UpdateSubscription(ctx context.Context, processorSubID, newProcessorPlanID, mode string) error {
	api, err := c.forMode(mode)
	if err != nil {
		return err
	}
	params := &stripe.SubscriptionParams{
		Items: []*stripe.SubscriptionItemsParams{{Price: ptrString(newProcessorPlanID)}},
	}
	params.Context = ctx
	_, err = processor.Retry(ctx, c.policy, func() (*stripe.Subscription, error) {
		s, callErr := api.Subscriptions.Update(processorSubID, params)
		return s, classify(callErr)
	})
	return err
}
