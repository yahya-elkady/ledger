package processor_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yahya-elkady/ledger/internal/processor"
)

// fastPolicy keeps retry tests quick.
var fastPolicy = processor.RetryPolicy{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond}

func TestRetryRetriesTransientThenSucceeds(t *testing.T) {
	calls := 0
	got, err := processor.Retry(context.Background(), fastPolicy, func() (string, error) {
		calls++
		if calls < 3 {
			return "", processor.NewError(processor.CodeUnavailable, true, nil, "boom")
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "ok" || calls != 3 {
		t.Errorf("got %q after %d calls, want ok after 3", got, calls)
	}
}

func TestRetryStopsOnNonRetryable(t *testing.T) {
	calls := 0
	_, err := processor.Retry(context.Background(), fastPolicy, func() (string, error) {
		calls++
		return "", processor.NewError(processor.CodeInvalidRequest, false, nil, "nope")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("non-retryable error retried %d times, want 1", calls)
	}
}

func TestRetryExhaustsAttempts(t *testing.T) {
	calls := 0
	_, err := processor.Retry(context.Background(), fastPolicy, func() (int, error) {
		calls++
		return 0, processor.NewError(processor.CodeUnavailable, true, nil, "always transient")
	})
	if err == nil {
		t.Fatal("expected error after exhausting attempts")
	}
	if calls != fastPolicy.MaxAttempts {
		t.Errorf("attempts = %d, want %d", calls, fastPolicy.MaxAttempts)
	}
}

func TestRetryHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	calls := 0
	_, err := processor.Retry(ctx, fastPolicy, func() (int, error) {
		calls++
		return 0, processor.NewError(processor.CodeUnavailable, true, nil, "transient")
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (cancelled before retry)", calls)
	}
}

func TestProcessorErrorShape(t *testing.T) {
	cause := errors.New("root cause")
	e := processor.NewError(processor.CodeRateLimited, true, cause, "throttled by %s", "vendor")
	if e.Code != processor.CodeRateLimited || !e.Retryable {
		t.Errorf("unexpected error fields: %+v", e)
	}
	if !errors.Is(e, cause) {
		t.Error("Unwrap should expose the cause for errors.Is")
	}
}

// muxStub records which adapter handled a call.
type muxStub struct {
	name      string
	seen      *string
	subsCalls *int
}

func (s muxStub) CreateCharge(_ context.Context, _ processor.ChargeRequest) (processor.ChargeResult, error) {
	*s.seen = s.name
	return processor.ChargeResult{ProcessorChargeID: s.name + "_ch", Status: "succeeded"}, nil
}
func (s muxStub) RefundCharge(_ context.Context, _, _ string, _ int64, _ string) (processor.RefundResult, error) {
	*s.seen = s.name
	return processor.RefundResult{Status: "succeeded"}, nil
}
func (s muxStub) CreatePayout(_ context.Context, _ processor.PayoutRequest) (processor.PayoutResult, error) {
	*s.seen = s.name
	return processor.PayoutResult{Status: "pending"}, nil
}
func (s muxStub) CreatePlan(_ context.Context, _ processor.PlanRequest) (string, error) {
	*s.subsCalls++
	return "plan_x", nil
}
func (s muxStub) CreateSubscription(_ context.Context, _ processor.SubscriptionRequest) (processor.SubscriptionResult, error) {
	*s.subsCalls++
	return processor.SubscriptionResult{ProcessorSubID: "sub_x", Status: "active"}, nil
}
func (s muxStub) CancelSubscription(_ context.Context, _ string, _ bool, _ string) error { return nil }
func (s muxStub) UpdateSubscription(_ context.Context, _, _, _ string) error             { return nil }

func TestMuxRoutesByProcessorName(t *testing.T) {
	var seen string
	subsCalls := 0
	stripe := muxStub{name: "stripe", seen: &seen, subsCalls: &subsCalls}
	plaid := muxStub{name: "plaid", seen: &seen}
	mux := processor.NewMux(stripe, map[string]processor.ChargePayoutProcessor{processor.Plaid: plaid})

	if _, err := mux.CreateCharge(context.Background(), processor.ChargeRequest{Processor: "plaid"}); err != nil {
		t.Fatalf("CreateCharge: %v", err)
	}
	if seen != "plaid" {
		t.Errorf("charge routed to %q, want plaid", seen)
	}

	if _, err := mux.CreatePayout(context.Background(), processor.PayoutRequest{Processor: "stripe"}); err != nil {
		t.Fatalf("CreatePayout: %v", err)
	}
	if seen != "stripe" {
		t.Errorf("payout routed to %q, want stripe", seen)
	}

	// Refund routes by explicit processor name.
	if _, err := mux.RefundCharge(context.Background(), "plaid", "ch_1", 100, "test"); err != nil {
		t.Fatalf("RefundCharge: %v", err)
	}
	if seen != "plaid" {
		t.Errorf("refund routed to %q, want plaid", seen)
	}

	// Subscriptions always go to Stripe regardless of any name.
	if _, err := mux.CreateSubscription(context.Background(), processor.SubscriptionRequest{}); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	if subsCalls != 1 {
		t.Errorf("subscription calls to stripe = %d, want 1", subsCalls)
	}
}

func TestMuxUnknownProcessor(t *testing.T) {
	var seen string
	subsCalls := 0
	stripe := muxStub{name: "stripe", seen: &seen, subsCalls: &subsCalls}
	mux := processor.NewMux(stripe, nil)

	_, err := mux.CreateCharge(context.Background(), processor.ChargeRequest{Processor: "acme"})
	if err == nil {
		t.Fatal("expected error for unknown processor")
	}
	var pe *processor.Error
	if !errors.As(err, &pe) || pe.Code != processor.CodeInvalidRequest {
		t.Errorf("got %v, want invalid_request processor error", err)
	}
}

func TestMuxDefaultsToStripe(t *testing.T) {
	var seen string
	subsCalls := 0
	stripe := muxStub{name: "stripe", seen: &seen, subsCalls: &subsCalls}
	mux := processor.NewMux(stripe, nil)

	// Empty processor name defaults to Stripe.
	if _, err := mux.CreateCharge(context.Background(), processor.ChargeRequest{}); err != nil {
		t.Fatalf("CreateCharge: %v", err)
	}
	if seen != "stripe" {
		t.Errorf("default route = %q, want stripe", seen)
	}
}
