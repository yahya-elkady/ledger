package payment_test

import (
	"context"
	"errors"
	"testing"

	"github.com/yahya-elkady/ledger/internal/ledger"
	"github.com/yahya-elkady/ledger/internal/payment"
)

// newPending is a small helper that builds a valid pending payment for tests.
func newPending(t *testing.T) *payment.Payment {
	t.Helper()
	p, err := payment.NewPayment("pay_1", ledger.MustMoney(1099, "USD"), "idem_1")
	if err != nil {
		t.Fatalf("NewPayment: unexpected error: %v", err)
	}
	return p
}

func TestNewPayment(t *testing.T) {
	t.Run("valid payment starts pending", func(t *testing.T) {
		p := newPending(t)
		if p.Status != payment.StatusPending {
			t.Errorf("Status = %q, want %q", p.Status, payment.StatusPending)
		}
		if p.ProviderRef != "" {
			t.Errorf("ProviderRef = %q, want empty on a fresh payment", p.ProviderRef)
		}
		if p.CreatedAt.IsZero() {
			t.Error("CreatedAt should be set on creation")
		}
		if !p.UpdatedAt.Equal(p.CreatedAt) {
			t.Errorf("UpdatedAt = %v, want equal to CreatedAt %v on creation", p.UpdatedAt, p.CreatedAt)
		}
	})

	t.Run("missing fields are rejected", func(t *testing.T) {
		cases := map[string]struct {
			id     string
			amount ledger.Money
			key    string
		}{
			"empty id":       {"", ledger.MustMoney(100, "USD"), "idem"},
			"empty key":      {"pay", ledger.MustMoney(100, "USD"), ""},
			"zero amount":    {"pay", ledger.MustMoney(0, "USD"), "idem"},
			"empty currency": {"pay", ledger.Money{Amount: 100}, "idem"},
		}
		for name, tc := range cases {
			t.Run(name, func(t *testing.T) {
				_, err := payment.NewPayment(tc.id, tc.amount, tc.key)
				if !errors.Is(err, payment.ErrInvalidPayment) {
					t.Errorf("got %v, want ErrInvalidPayment", err)
				}
			})
		}
	})
}

func TestLegalTransitions(t *testing.T) {
	t.Run("authorize: pending -> authorized", func(t *testing.T) {
		p := newPending(t)
		before := p.UpdatedAt
		if err := p.Authorize("ref_123"); err != nil {
			t.Fatalf("Authorize: unexpected error: %v", err)
		}
		if p.Status != payment.StatusAuthorized {
			t.Errorf("Status = %q, want %q", p.Status, payment.StatusAuthorized)
		}
		if p.ProviderRef != "ref_123" {
			t.Errorf("ProviderRef = %q, want %q", p.ProviderRef, "ref_123")
		}
		if !p.UpdatedAt.After(before) && !p.UpdatedAt.Equal(before) {
			t.Errorf("UpdatedAt = %v, want refreshed (>= %v)", p.UpdatedAt, before)
		}
	})

	t.Run("fail: pending -> failed", func(t *testing.T) {
		p := newPending(t)
		if err := p.Fail(); err != nil {
			t.Fatalf("Fail: unexpected error: %v", err)
		}
		if p.Status != payment.StatusFailed {
			t.Errorf("Status = %q, want %q", p.Status, payment.StatusFailed)
		}
	})

	t.Run("capture: authorized -> captured", func(t *testing.T) {
		p := newPending(t)
		mustAuthorize(t, p)
		if err := p.Capture(); err != nil {
			t.Fatalf("Capture: unexpected error: %v", err)
		}
		if p.Status != payment.StatusCaptured {
			t.Errorf("Status = %q, want %q", p.Status, payment.StatusCaptured)
		}
	})

	t.Run("void: authorized -> voided", func(t *testing.T) {
		p := newPending(t)
		mustAuthorize(t, p)
		if err := p.Void(); err != nil {
			t.Fatalf("Void: unexpected error: %v", err)
		}
		if p.Status != payment.StatusVoided {
			t.Errorf("Status = %q, want %q", p.Status, payment.StatusVoided)
		}
	})
}

// TestIllegalTransitions exercises every move that the spec says must be
// rejected, driving each target status and attempting the forbidden actions.
func TestIllegalTransitions(t *testing.T) {
	// act runs the named action against a payment, returning its error.
	act := func(p *payment.Payment, a string) error {
		switch a {
		case "authorize":
			return p.Authorize("ref")
		case "capture":
			return p.Capture()
		case "void":
			return p.Void()
		case "fail":
			return p.Fail()
		default:
			t.Fatalf("unknown action %q", a)
			return nil
		}
	}

	// build returns a fresh payment already driven to the requested status.
	build := func(t *testing.T, status payment.Status) *payment.Payment {
		t.Helper()
		p := newPending(t)
		switch status {
		case payment.StatusPending:
		case payment.StatusAuthorized:
			mustAuthorize(t, p)
		case payment.StatusCaptured:
			mustAuthorize(t, p)
			if err := p.Capture(); err != nil {
				t.Fatalf("setup Capture: %v", err)
			}
		case payment.StatusVoided:
			mustAuthorize(t, p)
			if err := p.Void(); err != nil {
				t.Fatalf("setup Void: %v", err)
			}
		case payment.StatusFailed:
			if err := p.Fail(); err != nil {
				t.Fatalf("setup Fail: %v", err)
			}
		}
		return p
	}

	cases := []struct {
		name   string
		status payment.Status
		action string
	}{
		{"capture from pending", payment.StatusPending, "capture"},
		{"void from pending", payment.StatusPending, "void"},
		{"authorize from authorized", payment.StatusAuthorized, "authorize"},
		{"fail from authorized", payment.StatusAuthorized, "fail"},
		{"capture from captured", payment.StatusCaptured, "capture"},
		{"void after capture", payment.StatusCaptured, "void"},
		{"authorize from captured", payment.StatusCaptured, "authorize"},
		{"capture from voided", payment.StatusVoided, "capture"},
		{"void from voided", payment.StatusVoided, "void"},
		{"authorize from voided", payment.StatusVoided, "authorize"},
		{"authorize from failed", payment.StatusFailed, "authorize"},
		{"capture from failed", payment.StatusFailed, "capture"},
		{"void from failed", payment.StatusFailed, "void"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := build(t, tc.status)
			before := p.Status
			err := act(p, tc.action)
			if !errors.Is(err, payment.ErrInvalidTransition) {
				t.Errorf("got %v, want ErrInvalidTransition", err)
			}
			if p.Status != before {
				t.Errorf("status changed on illegal transition: %q -> %q", before, p.Status)
			}
		})
	}
}

func TestIsTerminal(t *testing.T) {
	cases := map[payment.Status]bool{
		payment.StatusPending:    false,
		payment.StatusAuthorized: false,
		payment.StatusCaptured:   true,
		payment.StatusVoided:     true,
		payment.StatusFailed:     true,
	}
	for status, want := range cases {
		p := &payment.Payment{Status: status}
		if got := p.IsTerminal(); got != want {
			t.Errorf("IsTerminal(%q) = %v, want %v", status, got, want)
		}
	}
}

func mustAuthorize(t *testing.T, p *payment.Payment) {
	t.Helper()
	if err := p.Authorize("ref"); err != nil {
		t.Fatalf("setup Authorize: %v", err)
	}
}

func TestFakeProvider(t *testing.T) {
	ctx := context.Background()
	amount := ledger.MustMoney(1099, "USD")

	t.Run("succeeds by default", func(t *testing.T) {
		f := &payment.FakeProvider{}
		ref, err := f.Authorize(ctx, amount)
		if err != nil {
			t.Fatalf("Authorize: unexpected error: %v", err)
		}
		if ref == "" {
			t.Error("Authorize returned an empty providerRef")
		}
		if err := f.Capture(ctx, ref); err != nil {
			t.Errorf("Capture: unexpected error: %v", err)
		}
		if err := f.Void(ctx, ref); err != nil {
			t.Errorf("Void: unexpected error: %v", err)
		}
		if f.AuthorizeCalls != 1 || f.CaptureCalls != 1 || f.VoidCalls != 1 {
			t.Errorf("call counts = (%d,%d,%d), want (1,1,1)",
				f.AuthorizeCalls, f.CaptureCalls, f.VoidCalls)
		}
	})

	t.Run("returns the configured reference", func(t *testing.T) {
		f := &payment.FakeProvider{AuthorizeRef: "ref_custom"}
		ref, err := f.Authorize(ctx, amount)
		if err != nil {
			t.Fatalf("Authorize: unexpected error: %v", err)
		}
		if ref != "ref_custom" {
			t.Errorf("ref = %q, want %q", ref, "ref_custom")
		}
	})

	t.Run("declines on demand", func(t *testing.T) {
		f := &payment.FakeProvider{DeclineAuthorize: true}
		ref, err := f.Authorize(ctx, amount)
		if !errors.Is(err, payment.ErrDeclined) {
			t.Errorf("got %v, want ErrDeclined", err)
		}
		if ref != "" {
			t.Errorf("ref = %q, want empty on decline", ref)
		}
	})

	t.Run("errors on demand", func(t *testing.T) {
		boom := errors.New("processor unreachable")
		f := &payment.FakeProvider{
			AuthorizeErr: boom,
			CaptureErr:   boom,
			VoidErr:      boom,
		}
		if _, err := f.Authorize(ctx, amount); !errors.Is(err, boom) {
			t.Errorf("Authorize err = %v, want %v", err, boom)
		}
		if err := f.Capture(ctx, "ref"); !errors.Is(err, boom) {
			t.Errorf("Capture err = %v, want %v", err, boom)
		}
		if err := f.Void(ctx, "ref"); !errors.Is(err, boom) {
			t.Errorf("Void err = %v, want %v", err, boom)
		}
	})

	t.Run("injected error takes precedence over decline", func(t *testing.T) {
		boom := errors.New("processor unreachable")
		f := &payment.FakeProvider{DeclineAuthorize: true, AuthorizeErr: boom}
		if _, err := f.Authorize(ctx, amount); !errors.Is(err, boom) {
			t.Errorf("got %v, want injected error %v", err, boom)
		}
	})
}
