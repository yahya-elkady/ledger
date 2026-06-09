package processor

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

// Fake is a controllable Processor for tests and local development. By default
// every operation succeeds and returns synthetic reference ids. Tests flip the
// Decline*/Err* fields to exercise failure paths deterministically.
type Fake struct {
	// DeclineCharge makes CreateCharge return a failed ChargeResult.
	DeclineCharge bool
	// ChargeErr / RefundErr / etc. inject transport-style errors.
	ChargeErr       error
	RefundErr       error
	PlanErr         error
	SubscriptionErr error
	PayoutErr       error

	// Counters for assertions.
	ChargeCalls int32
	RefundCalls int32
	PayoutCalls int32

	seq uint64
}

var _ Processor = (*Fake)(nil)

func (f *Fake) next(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, atomic.AddUint64(&f.seq, 1))
}

// CreateCharge returns a succeeded charge, a failed one (DeclineCharge), or an
// error (ChargeErr).
func (f *Fake) CreateCharge(_ context.Context, req ChargeRequest) (ChargeResult, error) {
	atomic.AddInt32(&f.ChargeCalls, 1)
	if f.ChargeErr != nil {
		return ChargeResult{}, f.ChargeErr
	}
	if f.DeclineCharge {
		return ChargeResult{
			ProcessorChargeID: f.next("ch"),
			Status:            "failed",
			FailureCode:       "card_declined",
			FailureMessage:    "the card was declined",
		}, nil
	}
	return ChargeResult{ProcessorChargeID: f.next("ch"), Status: "succeeded"}, nil
}

// RefundCharge returns a succeeded refund or an error (RefundErr).
func (f *Fake) RefundCharge(_ context.Context, _ string, _ int64, _ string) (RefundResult, error) {
	atomic.AddInt32(&f.RefundCalls, 1)
	if f.RefundErr != nil {
		return RefundResult{}, f.RefundErr
	}
	return RefundResult{ProcessorRefundID: f.next("re"), Status: "succeeded"}, nil
}

// CreatePlan returns a synthetic plan id or an error (PlanErr).
func (f *Fake) CreatePlan(_ context.Context, _ PlanRequest) (string, error) {
	if f.PlanErr != nil {
		return "", f.PlanErr
	}
	return f.next("plan"), nil
}

// CreateSubscription returns an active subscription with a 30-day period, or an
// error (SubscriptionErr).
func (f *Fake) CreateSubscription(_ context.Context, req SubscriptionRequest) (SubscriptionResult, error) {
	if f.SubscriptionErr != nil {
		return SubscriptionResult{}, f.SubscriptionErr
	}
	now := time.Now().UTC()
	status := "active"
	if !req.TrialEnd.IsZero() {
		status = "trialing"
	}
	return SubscriptionResult{
		ProcessorSubID:     f.next("sub"),
		Status:             status,
		CurrentPeriodStart: now,
		CurrentPeriodEnd:   now.AddDate(0, 1, 0),
	}, nil
}

// CancelSubscription succeeds unless SubscriptionErr is set.
func (f *Fake) CancelSubscription(_ context.Context, _ string, _ bool, _ string) error {
	return f.SubscriptionErr
}

// UpdateSubscription succeeds unless SubscriptionErr is set.
func (f *Fake) UpdateSubscription(_ context.Context, _, _, _ string) error {
	return f.SubscriptionErr
}

// CreatePayout returns a pending payout (arriving in 2 days) or an error.
func (f *Fake) CreatePayout(_ context.Context, _ PayoutRequest) (PayoutResult, error) {
	atomic.AddInt32(&f.PayoutCalls, 1)
	if f.PayoutErr != nil {
		return PayoutResult{}, f.PayoutErr
	}
	return PayoutResult{
		ProcessorPayoutID: f.next("po"),
		Status:            "pending",
		ArrivalDate:       time.Now().UTC().AddDate(0, 0, 2),
	}, nil
}
