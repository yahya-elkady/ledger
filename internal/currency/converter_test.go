package currency_test

import (
	"testing"

	"github.com/yahya-elkady/ledger/internal/currency"
)

func TestValidateCurrency(t *testing.T) {
	cases := []struct {
		code string
		want bool
	}{
		{"USD", true},
		{"JPY", true},
		{"usd", true}, // case-insensitive
		{"jpy", true},
		{"XXX", false},
		{"", false},
		{"US", false},
		{"USDD", false},
	}
	for _, tc := range cases {
		if got := currency.ValidateCurrency(tc.code); got != tc.want {
			t.Errorf("ValidateCurrency(%q) = %v, want %v", tc.code, got, tc.want)
		}
	}
}

func TestSupportedCurrenciesCompleteness(t *testing.T) {
	// The spec mandates at minimum these twelve currencies.
	required := []string{"USD", "EUR", "GBP", "CAD", "AUD", "JPY", "CHF", "MXN", "BRL", "INR", "SGD", "HKD"}
	for _, code := range required {
		c, ok := currency.SupportedCurrencies[code]
		if !ok {
			t.Errorf("required currency %q missing from SupportedCurrencies", code)
			continue
		}
		if c.Code != code {
			t.Errorf("SupportedCurrencies[%q].Code = %q, want %q", code, c.Code, code)
		}
		if c.MinorUnits < 0 {
			t.Errorf("%q MinorUnits = %d, want >= 0", code, c.MinorUnits)
		}
		if c.Name == "" || c.Symbol == "" {
			t.Errorf("%q has empty Name or Symbol", code)
		}
	}
}

func TestFormatAmount(t *testing.T) {
	cases := []struct {
		amount int64
		code   string
		want   string
	}{
		{1000, "USD", "$10.00"},
		{1099, "USD", "$10.99"},
		{5, "USD", "$0.05"},
		{0, "USD", "$0.00"},
		{1000, "JPY", "¥1000"}, // zero-decimal: no division by 100
		{1, "JPY", "¥1"},
		{1099, "EUR", "€10.99"},
		{2500, "GBP", "£25.00"},
		{-1099, "USD", "-$10.99"},
		{1099, "ZZZ", "1099 ZZZ"}, // unknown currency fallback
	}
	for _, tc := range cases {
		if got := currency.FormatAmount(tc.amount, tc.code); got != tc.want {
			t.Errorf("FormatAmount(%d, %q) = %q, want %q", tc.amount, tc.code, got, tc.want)
		}
	}
}

func TestConvertMinorUnits(t *testing.T) {
	cases := []struct {
		amount int64
		code   string
		want   float64
	}{
		{1099, "USD", 10.99},
		{0, "USD", 0},
		{1000, "JPY", 1000}, // zero-decimal stays whole
		{500, "GBP", 5.0},
		{1234, "XXX", 1234}, // unknown → treated as zero-decimal
	}
	for _, tc := range cases {
		if got := currency.ConvertMinorUnits(tc.amount, tc.code); got != tc.want {
			t.Errorf("ConvertMinorUnits(%d, %q) = %v, want %v", tc.amount, tc.code, got, tc.want)
		}
	}
}

func TestLookupCaseInsensitive(t *testing.T) {
	c, ok := currency.Lookup("eur")
	if !ok {
		t.Fatal("Lookup(\"eur\") not found")
	}
	if c.Code != "EUR" {
		t.Errorf("Lookup(\"eur\").Code = %q, want EUR", c.Code)
	}
}
