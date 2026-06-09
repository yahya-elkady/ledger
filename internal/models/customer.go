package models

import (
	"encoding/json"
	"time"
)

// Customer is a merchant's end-user. Metadata is opaque merchant-supplied JSON,
// passed through verbatim. Customers are scoped to a merchant (not to test/live
// mode — the table has no mode column).
type Customer struct {
	ID         string          `json:"id"`
	MerchantID string          `json:"merchant_id"`
	Email      string          `json:"email,omitempty"`
	Name       string          `json:"name,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}
