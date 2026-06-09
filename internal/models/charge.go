package models

import (
	"encoding/json"
	"time"
)

// Charge is a one-time payment. Amounts are integer minor units (cents); the
// money never becomes a float.
type Charge struct {
	ID              string          `json:"id"`
	MerchantID      string          `json:"merchant_id"`
	CustomerID      string          `json:"customer_id,omitempty"`
	PaymentMethodID string          `json:"payment_method_id,omitempty"`
	Amount          int64           `json:"amount"`
	Currency        string          `json:"currency"`
	Status          string          `json:"status"`
	Processor       string          `json:"processor"`
	ProcessorChargeID string        `json:"processor_charge_id,omitempty"`
	Mode            string          `json:"mode"`
	FailureCode     string          `json:"failure_code,omitempty"`
	FailureMessage  string          `json:"failure_message,omitempty"`
	RefundedAmount  int64           `json:"refunded_amount"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}
