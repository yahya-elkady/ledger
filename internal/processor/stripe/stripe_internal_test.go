package stripe

import (
	"context"
	"errors"
	"testing"

	"github.com/yahya-elkady/ledger/internal/processor"
)

func TestModeAwareKeySelection(t *testing.T) {
	// Only a test key configured: a live-mode call must fail fast with an auth
	// error before any network request (a live key is never substituted).
	c := New(Config{TestKey: "sk_test_x"})

	_, err := c.CreateCharge(context.Background(), processor.ChargeRequest{Mode: "live", Amount: 100, Currency: "USD"})
	var pe *processor.Error
	if !errors.As(err, &pe) || pe.Code != processor.CodeAuth {
		t.Errorf("live-mode call with no live key: got %v, want auth error", err)
	}
}

func TestUnconfiguredTestMode(t *testing.T) {
	c := New(Config{LiveKey: "sk_live_x"})
	_, err := c.CreateCharge(context.Background(), processor.ChargeRequest{Mode: "test", Amount: 100, Currency: "USD"})
	var pe *processor.Error
	if !errors.As(err, &pe) || pe.Code != processor.CodeAuth {
		t.Errorf("test-mode call with no test key: got %v, want auth error", err)
	}
}
