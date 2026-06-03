package payments_test

import (
    "payments"

	"errors"
	"testing"
)

func TestNewMoney(t *testing.T) {
	t.Run("valid amount and currency", func(t *testing.T) {
		m, err := payments.NewMoney(1099, "USD")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.Amount != 1099 {
			t.Errorf("Amount = %d, want 1099", m.Amount)
		}
		if m.Currency != "USD" {
			t.Errorf("Currency = %q, want %q", m.Currency, "USD")
		}
	})

	t.Run("zero amount is valid", func(t *testing.T) {
		m, err := payments.NewMoney(0, "USD")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !m.IsZero() {
			t.Error("expected IsZero() == true")
		}
	})

	t.Run("negative amount is rejected", func(t *testing.T) {
		_, err := payments.NewMoney(-1, "USD")
		if !errors.Is(err, payments.ErrNegativeAmount) {
			t.Errorf("got %v, want ErrNegativeAmount", err)
		}
	})

	t.Run("empty currency is rejected", func(t *testing.T) {
		_, err := payments.NewMoney(100, "")
		if !errors.Is(err, payments.ErrInvalidCurrency) {
			t.Errorf("got %v, want ErrInvalidCurrency", err)
		}
	})
}

func TestMustMoney_panics_on_invalid(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic, got none")
		}
	}()
	payments.MustMoney(-1, "USD")
}

func TestMoney_Add(t *testing.T) {
	t.Run("same currency sums correctly", func(t *testing.T) {
		a := payments.MustMoney(500, "USD")
		b := payments.MustMoney(299, "USD")
		got, err := a.Add(b)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := payments.MustMoney(799, "USD")
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("adding zero is identity", func(t *testing.T) {
		a := payments.MustMoney(1000, "USD")
		zero := payments.MustMoney(0, "USD")
		got, err := a.Add(zero)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.Equal(a) {
			t.Errorf("got %v, want %v", got, a)
		}
	})

	t.Run("currency mismatch is rejected", func(t *testing.T) {
		a := payments.MustMoney(100, "USD")
		b := payments.MustMoney(100, "EUR")
		_, err := a.Add(b)
		if !errors.Is(err, payments.ErrCurrencyMismatch) {
			t.Errorf("got %v, want ErrCurrencyMismatch", err)
		}
	})

	t.Run("no floating-point rounding", func(t *testing.T) {
		// Amounts that would produce rounding errors as float64.
		// 0.1 + 0.2 in float64 != 0.3; as int64 minor units it must be exact.
		a := payments.MustMoney(10, "USD") // $0.10
		b := payments.MustMoney(20, "USD") // $0.20
		got, err := a.Add(b)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := payments.MustMoney(30, "USD") // $0.30
		if !got.Equal(want) {
			t.Errorf("got %v, want %v — floating-point contamination suspected", got, want)
		}
	})
}

func TestMoney_Sub(t *testing.T) {
	t.Run("valid subtraction", func(t *testing.T) {
		a := payments.MustMoney(1000, "USD")
		b := payments.MustMoney(300, "USD")
		got, err := a.Sub(b)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := payments.MustMoney(700, "USD")
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("subtracting same amount yields zero", func(t *testing.T) {
		a := payments.MustMoney(500, "USD")
		got, err := a.Sub(a)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.IsZero() {
			t.Errorf("expected zero, got %v", got)
		}
	})

	t.Run("result going negative is rejected", func(t *testing.T) {
		a := payments.MustMoney(100, "USD")
		b := payments.MustMoney(101, "USD")
		_, err := a.Sub(b)
		if !errors.Is(err, payments.ErrNegativeAmount) {
			t.Errorf("got %v, want ErrNegativeAmount", err)
		}
	})

	t.Run("currency mismatch is rejected", func(t *testing.T) {
		a := payments.MustMoney(500, "USD")
		b := payments.MustMoney(100, "GBP")
		_, err := a.Sub(b)
		if !errors.Is(err, payments.ErrCurrencyMismatch) {
			t.Errorf("got %v, want ErrCurrencyMismatch", err)
		}
	})
}

func TestMoney_Equal(t *testing.T) {
	cases := []struct {
		name string
		a, b payments.Money
		want bool
	}{
		{"same amount and currency", payments.MustMoney(100, "USD"), payments.MustMoney(100, "USD"), true},
		{"different amount", payments.MustMoney(100, "USD"), payments.MustMoney(200, "USD"), false},
		{"different currency", payments.MustMoney(100, "USD"), payments.MustMoney(100, "EUR"), false},
		{"both zero same currency", payments.MustMoney(0, "USD"), payments.MustMoney(0, "USD"), true},
		{"both zero different currency", payments.MustMoney(0, "USD"), payments.MustMoney(0, "EUR"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.Equal(tc.b); got != tc.want {
				t.Errorf("Equal() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMoney_Zero(t *testing.T) {
	m := payments.MustMoney(500, "EUR")
	z := m.Zero()
	if z.Amount != 0 {
		t.Errorf("Zero().Amount = %d, want 0", z.Amount)
	}
	if z.Currency != "EUR" {
		t.Errorf("Zero().Currency = %q, want %q", z.Currency, "EUR")
	}
}

func TestMoney_String(t *testing.T) {
	m := payments.MustMoney(1099, "USD")
	got := m.String()
	want := "1099 USD"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}