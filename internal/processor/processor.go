// Package processor is the abstraction over external payment processors
// (Stripe, Plaid). The API handlers depend on these interfaces, never on a
// concrete SDK, so the real adapters (Phase 6) and the test fake are
// interchangeable. Mode (test/live) is threaded through every call so an
// implementation can select the correct credentials and environment.
//
// PCI-DSS: processors handle raw card and bank data; this service never
// receives, stores, or logs card numbers, CVVs, or bank credentials — only
// opaque processor tokens and reference ids cross this boundary.
package processor

import (
	"context"
	"errors"
	"time"
)

// Processor names recognized by the API.
const (
	Stripe = "stripe"
	Plaid  = "plaid"
)

// ErrDeclined is a normal business outcome: the processor refused the operation
// (e.g. a declined card). It is distinct from a transport/processor error, which
// is surfaced as a generic error.
var ErrDeclined = errors.New("processor declined")

// ChargeRequest asks a processor to take a one-time payment.
type ChargeRequest struct {
	Amount            int64 // minor units (cents)
	Currency          string
	Mode              string // test | live
	ProcessorMethodID string // tokenized payment method at the processor
	Description       string
}

// ChargeResult is the processor's response to a charge.
type ChargeResult struct {
	ProcessorChargeID string
	Status            string // "succeeded" | "failed"
	FailureCode       string
	FailureMessage    string
}

// RefundResult is the processor's response to a refund.
type RefundResult struct {
	ProcessorRefundID string
	Status            string // "succeeded" | "failed"
}

// ChargeProcessor creates and refunds one-time charges.
//
// PCI-DSS: implementations call the external card processor; no PAN/CVV is ever
// passed through this interface — only a ProcessorMethodID token.
type ChargeProcessor interface {
	CreateCharge(ctx context.Context, req ChargeRequest) (ChargeResult, error)
	RefundCharge(ctx context.Context, processorChargeID string, amount int64, mode string) (RefundResult, error)
}

// PlanRequest creates a recurring price/plan at the processor.
type PlanRequest struct {
	Name          string
	Amount        int64
	Currency      string
	Interval      string // day | week | month | year
	IntervalCount int
	Mode          string
}

// SubscriptionRequest starts a subscription for a customer on a plan.
type SubscriptionRequest struct {
	ProcessorPlanID   string
	ProcessorMethodID string
	Mode              string
	TrialEnd          time.Time // zero => no trial
}

// SubscriptionResult is the processor's response to creating a subscription.
type SubscriptionResult struct {
	ProcessorSubID     string
	Status             string // active | trialing | ...
	CurrentPeriodStart time.Time
	CurrentPeriodEnd   time.Time
}

// SubscriptionProcessor manages recurring billing.
type SubscriptionProcessor interface {
	CreatePlan(ctx context.Context, req PlanRequest) (planID string, err error)
	CreateSubscription(ctx context.Context, req SubscriptionRequest) (SubscriptionResult, error)
	CancelSubscription(ctx context.Context, processorSubID string, atPeriodEnd bool, mode string) error
	UpdateSubscription(ctx context.Context, processorSubID, newProcessorPlanID, mode string) error
}

// PayoutRequest moves funds to a merchant bank account.
type PayoutRequest struct {
	Amount          int64
	Currency        string
	Mode            string
	ProcessorAcctID string
}

// PayoutResult is the processor's response to a payout.
type PayoutResult struct {
	ProcessorPayoutID string
	Status            string // pending | in_transit | paid | failed
	ArrivalDate       time.Time
}

// PayoutProcessor initiates payouts to bank accounts.
type PayoutProcessor interface {
	CreatePayout(ctx context.Context, req PayoutRequest) (PayoutResult, error)
}

// Processor is the union of all processor capabilities, satisfied by the
// per-vendor adapters and by the test fake.
type Processor interface {
	ChargeProcessor
	SubscriptionProcessor
	PayoutProcessor
}
