package ledger

import (
	"errors"
	"fmt"
)

// ErrNegativeAmount is returned when an operation would produce a negative Money value.
// By design, Money is always non-negative; sign is expressed through Entry.Direction.
var ErrNegativeAmount = errors.New("money amount cannot be negative")

// ErrCurrencyMismatch is returned when an arithmetic operation is attempted on
// Money values with different currency codes.
var ErrCurrencyMismatch = errors.New("currency mismatch")

// ErrInvalidCurrency is returned when a currency code is empty or otherwise invalid.
var ErrInvalidCurrency = errors.New("invalid currency code")

// Money represents a non-negative monetary amount in minor units (e.g. cents)
// paired with an ISO 4217 currency code.
//
// Amount is stored as int64 minor units to avoid floating-point rounding errors.
// For example, $10.99 USD is represented as Amount=1099, Currency="USD".
//
// Money is intentionally non-negative. Sign (debit vs. credit) belongs to
// Entry.Direction, not here. Any operation that would produce a negative amount
// returns ErrNegativeAmount.
type Money struct {
	Amount   int64  // non-negative, in minor units
	Currency string // ISO 4217, e.g. "USD", "EUR", "GBP"
}

// NewMoney constructs a Money value, validating that amount is non-negative
// and currency is non-empty.
func NewMoney(amount int64, currency string) (Money, error) {
	if currency == "" {
		return Money{}, ErrInvalidCurrency
	}
	if amount < 0 {
		return Money{}, ErrNegativeAmount
	}
	return Money{Amount: amount, Currency: currency}, nil
}

// MustMoney constructs a Money value and panics on invalid input.
// Intended for use in tests and package-level constants only.
func MustMoney(amount int64, currency string) Money {
	m, err := NewMoney(amount, currency)
	if err != nil {
		panic(fmt.Sprintf("MustMoney(%d, %q): %v", amount, currency, err))
	}
	return m
}

// Add returns the sum of m and other. Both must share the same currency.
// Returns ErrCurrencyMismatch if currencies differ.
func (m Money) Add(other Money) (Money, error) {
	if err := m.sameCurrency(other); err != nil {
		return Money{}, err
	}
	return Money{Amount: m.Amount + other.Amount, Currency: m.Currency}, nil
}

// Sub returns m minus other. Both must share the same currency and the result
// must be non-negative. Returns ErrNegativeAmount if other exceeds m.
func (m Money) Sub(other Money) (Money, error) {
	if err := m.sameCurrency(other); err != nil {
		return Money{}, err
	}
	if other.Amount > m.Amount {
		return Money{}, fmt.Errorf("%w: %d - %d", ErrNegativeAmount, m.Amount, other.Amount)
	}
	return Money{Amount: m.Amount - other.Amount, Currency: m.Currency}, nil
}

// IsZero reports whether the amount is zero.
func (m Money) IsZero() bool {
	return m.Amount == 0
}

// Equal reports whether m and other have the same amount and currency.
func (m Money) Equal(other Money) bool {
	return m.Amount == other.Amount && m.Currency == other.Currency
}

// String returns a human-readable representation, e.g. "1099 USD".
// For display formatting with decimal points, use a separate presenter;
// this is intentionally raw to avoid locale assumptions.
func (m Money) String() string {
	return fmt.Sprintf("%d %s", m.Amount, m.Currency)
}

// Zero returns a zero-value Money in the same currency.
func (m Money) Zero() Money {
	return Money{Amount: 0, Currency: m.Currency}
}

// sameCurrency returns an error if m and other have different currency codes.
func (m Money) sameCurrency(other Money) error {
	if m.Currency != other.Currency {
		return fmt.Errorf("%w: %s vs %s", ErrCurrencyMismatch, m.Currency, other.Currency)
	}
	return nil
}