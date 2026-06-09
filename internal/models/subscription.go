package models

import (
	"encoding/json"
	"time"
)

// Plan is a recurring price a merchant can subscribe customers to.
type Plan struct {
	ID              string    `json:"id"`
	MerchantID      string    `json:"merchant_id"`
	Name            string    `json:"name"`
	Amount          int64     `json:"amount"`
	Currency        string    `json:"currency"`
	Interval        string    `json:"interval"`
	IntervalCount   int       `json:"interval_count"`
	ProcessorPlanID string    `json:"processor_plan_id,omitempty"`
	Mode            string    `json:"mode"`
	CreatedAt       time.Time `json:"created_at"`
}

// Subscription is a customer's recurring billing arrangement on a plan.
type Subscription struct {
	ID                 string          `json:"id"`
	MerchantID         string          `json:"merchant_id"`
	CustomerID         string          `json:"customer_id"`
	PlanID             string          `json:"plan_id"`
	PaymentMethodID    string          `json:"payment_method_id"`
	Status             string          `json:"status"`
	ProcessorSubID     string          `json:"processor_sub_id,omitempty"`
	CurrentPeriodStart *time.Time      `json:"current_period_start,omitempty"`
	CurrentPeriodEnd   *time.Time      `json:"current_period_end,omitempty"`
	TrialEnd           *time.Time      `json:"trial_end,omitempty"`
	CanceledAt         *time.Time      `json:"canceled_at,omitempty"`
	Mode               string          `json:"mode"`
	Metadata           json.RawMessage `json:"metadata,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}
