package models

import "time"

// BankAccount is a merchant payout destination.
type BankAccount struct {
	ID              string    `json:"id"`
	MerchantID      string    `json:"merchant_id"`
	Processor       string    `json:"processor"`
	ProcessorAcctID string    `json:"processor_acct_id"`
	Last4           string    `json:"last4,omitempty"`
	BankName        string    `json:"bank_name,omitempty"`
	Currency        string    `json:"currency,omitempty"`
	IsDefault       bool      `json:"is_default"`
	CreatedAt       time.Time `json:"created_at"`
}

// Payout is a transfer of funds to a merchant bank account.
type Payout struct {
	ID                string     `json:"id"`
	MerchantID        string     `json:"merchant_id"`
	BankAccountID     string     `json:"bank_account_id"`
	Amount            int64      `json:"amount"`
	Currency          string     `json:"currency"`
	Status            string     `json:"status"`
	Processor         string     `json:"processor"`
	ProcessorPayoutID string     `json:"processor_payout_id,omitempty"`
	Mode              string     `json:"mode"`
	FailureMessage    string     `json:"failure_message,omitempty"`
	ArrivalDate       *time.Time `json:"arrival_date,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}
