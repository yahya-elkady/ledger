package processor

import "context"

// Mux routes each call to the right vendor adapter by processor name. Charges
// and payouts are routed per request (a merchant may use Stripe for cards and
// Plaid for ACH); recurring billing is Stripe-only, so subscription/plan calls
// always go to the Stripe adapter.
//
// Mux satisfies the full Processor interface, so the API handlers keep depending
// on a single processor.Processor and remain unaware of vendor selection.
type Mux struct {
	stripe Processor // handles subscriptions + its own charges/payouts
	byName map[string]ChargePayoutProcessor
}

var _ Processor = (*Mux)(nil)

// NewMux builds a router. stripe is required (it owns recurring billing); extra
// named charge/payout adapters (e.g. Plaid) are registered for routing.
func NewMux(stripe Processor, others map[string]ChargePayoutProcessor) *Mux {
	byName := map[string]ChargePayoutProcessor{Stripe: stripe}
	for name, p := range others {
		byName[name] = p
	}
	return &Mux{stripe: stripe, byName: byName}
}

// adapter resolves a charge/payout adapter by name, defaulting to Stripe.
func (m *Mux) adapter(name string) (ChargePayoutProcessor, error) {
	if name == "" {
		return m.stripe, nil
	}
	p, ok := m.byName[name]
	if !ok {
		return nil, newError(CodeInvalidRequest, false, nil, "unknown processor %q", name)
	}
	return p, nil
}

// CreateCharge routes by req.Processor.
func (m *Mux) CreateCharge(ctx context.Context, req ChargeRequest) (ChargeResult, error) {
	p, err := m.adapter(req.Processor)
	if err != nil {
		return ChargeResult{}, err
	}
	return p.CreateCharge(ctx, req)
}

// RefundCharge routes by processorName (the processor that made the charge).
func (m *Mux) RefundCharge(ctx context.Context, processorName, processorChargeID string, amount int64, mode string) (RefundResult, error) {
	p, err := m.adapter(processorName)
	if err != nil {
		return RefundResult{}, err
	}
	return p.RefundCharge(ctx, processorName, processorChargeID, amount, mode)
}

// CreatePayout routes by req.Processor.
func (m *Mux) CreatePayout(ctx context.Context, req PayoutRequest) (PayoutResult, error) {
	p, err := m.adapter(req.Processor)
	if err != nil {
		return PayoutResult{}, err
	}
	return p.CreatePayout(ctx, req)
}

// Recurring billing is Stripe-only.
func (m *Mux) CreatePlan(ctx context.Context, req PlanRequest) (string, error) {
	return m.stripe.CreatePlan(ctx, req)
}

func (m *Mux) CreateSubscription(ctx context.Context, req SubscriptionRequest) (SubscriptionResult, error) {
	return m.stripe.CreateSubscription(ctx, req)
}

func (m *Mux) CancelSubscription(ctx context.Context, processorSubID string, atPeriodEnd bool, mode string) error {
	return m.stripe.CancelSubscription(ctx, processorSubID, atPeriodEnd, mode)
}

func (m *Mux) UpdateSubscription(ctx context.Context, processorSubID, newProcessorPlanID, mode string) error {
	return m.stripe.UpdateSubscription(ctx, processorSubID, newProcessorPlanID, mode)
}
