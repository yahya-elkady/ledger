// Package currency provides ISO 4217 currency metadata, validation, and
// display-only formatting helpers.
//
// All amounts stored and processed by this service are integer minor units of
// the currency (cents for USD, pence for GBP, whole yen for JPY). Amounts are
// NEVER represented as floats internally. The float-returning helper here is for
// human-facing display only.
//
// This package does NOT perform foreign-exchange conversion. Converting an amount
// from one currency to another requires an explicit, dated FX rate from an
// authoritative source; doing so silently server-side would corrupt ledgers.
package currency

import (
	"fmt"
	"math"
	"strings"
)

// Currency describes an ISO 4217 currency supported by the service.
type Currency struct {
	// Code is the ISO 4217 alphabetic code, e.g. "USD".
	Code string
	// Name is the human-readable currency name, e.g. "US Dollar".
	Name string
	// Symbol is the display symbol, e.g. "$" or "¥".
	Symbol string
	// MinorUnits is the number of decimal digits in the currency's minor unit:
	// 2 for USD (cents), 0 for JPY (no subunit in common use).
	MinorUnits int
	// MinAmount is the smallest chargeable amount in minor units, mirroring the
	// processors' per-currency minimums (e.g. 50 = $0.50 for USD).
	MinAmount int64
}

// SupportedCurrencies maps ISO 4217 code → Currency metadata for every currency
// this service accepts. Codes are uppercase.
var SupportedCurrencies = map[string]Currency{
	"USD": {Code: "USD", Name: "US Dollar", Symbol: "$", MinorUnits: 2, MinAmount: 50},
	"EUR": {Code: "EUR", Name: "Euro", Symbol: "€", MinorUnits: 2, MinAmount: 50},
	"GBP": {Code: "GBP", Name: "Pound Sterling", Symbol: "£", MinorUnits: 2, MinAmount: 30},
	"CAD": {Code: "CAD", Name: "Canadian Dollar", Symbol: "$", MinorUnits: 2, MinAmount: 50},
	"AUD": {Code: "AUD", Name: "Australian Dollar", Symbol: "$", MinorUnits: 2, MinAmount: 50},
	"JPY": {Code: "JPY", Name: "Japanese Yen", Symbol: "¥", MinorUnits: 0, MinAmount: 50},
	"CHF": {Code: "CHF", Name: "Swiss Franc", Symbol: "CHF", MinorUnits: 2, MinAmount: 50},
	"MXN": {Code: "MXN", Name: "Mexican Peso", Symbol: "$", MinorUnits: 2, MinAmount: 1000},
	"BRL": {Code: "BRL", Name: "Brazilian Real", Symbol: "R$", MinorUnits: 2, MinAmount: 50},
	"INR": {Code: "INR", Name: "Indian Rupee", Symbol: "₹", MinorUnits: 2, MinAmount: 50},
	"SGD": {Code: "SGD", Name: "Singapore Dollar", Symbol: "$", MinorUnits: 2, MinAmount: 50},
	"HKD": {Code: "HKD", Name: "Hong Kong Dollar", Symbol: "$", MinorUnits: 2, MinAmount: 400},
}

// ValidateCurrency reports whether code is a supported ISO 4217 currency.
// The comparison is case-insensitive; callers should still normalize codes to
// uppercase before persisting.
func ValidateCurrency(code string) bool {
	_, ok := SupportedCurrencies[strings.ToUpper(code)]
	return ok
}

// Lookup returns the Currency metadata for code (case-insensitive) and whether
// it was found.
func Lookup(code string) (Currency, bool) {
	c, ok := SupportedCurrencies[strings.ToUpper(code)]
	return c, ok
}

// FormatAmount renders amount (in minor units) as a human-readable string for
// the given currency, e.g. FormatAmount(1000, "USD") == "$10.00" and
// FormatAmount(1000, "JPY") == "¥1000". Unknown currencies fall back to
// "<amount> <CODE>" so the value is never silently dropped.
func FormatAmount(amount int64, code string) string {
	c, ok := Lookup(code)
	if !ok {
		return fmt.Sprintf("%d %s", amount, strings.ToUpper(code))
	}

	neg := amount < 0
	abs := amount
	if neg {
		abs = -abs
	}

	var body string
	if c.MinorUnits == 0 {
		body = fmt.Sprintf("%s%d", c.Symbol, abs)
	} else {
		divisor := int64(math.Pow10(c.MinorUnits))
		major := abs / divisor
		minor := abs % divisor
		body = fmt.Sprintf("%s%d.%0*d", c.Symbol, major, c.MinorUnits, minor)
	}

	if neg {
		return "-" + body
	}
	return body
}

// ConvertMinorUnits converts an integer minor-unit amount to its major-unit
// decimal value for DISPLAY ONLY (e.g. 1099 USD → 10.99). The result is a float
// and must never be used for arithmetic, storage, or processor calls. Unknown
// currencies are treated as zero-decimal (the amount is returned unchanged).
func ConvertMinorUnits(amount int64, code string) float64 {
	c, ok := Lookup(code)
	if !ok || c.MinorUnits == 0 {
		return float64(amount)
	}
	return float64(amount) / math.Pow10(c.MinorUnits)
}
