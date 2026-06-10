package handlers

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/yahya-elkady/ledger/internal/api/middleware"
	"github.com/yahya-elkady/ledger/internal/api/respond"
	"github.com/yahya-elkady/ledger/internal/currency"
	"github.com/yahya-elkady/ledger/internal/models"
	"github.com/yahya-elkady/ledger/internal/processor"
	"github.com/yahya-elkady/ledger/internal/store"
)

// --- bank accounts ---------------------------------------------------------

type bankAccountRequest struct {
	Processor       string `json:"processor"`
	ProcessorAcctID string `json:"processor_acct_id"`
	Last4           string `json:"last4"`
	BankName        string `json:"bank_name"`
	Currency        string `json:"currency"`
	IsDefault       bool   `json:"is_default"`
}

// CreateBankAccount registers a payout destination for the merchant.
//
// PCI-DSS: bank credentials are handled by the processor; we store only an
// opaque processor account id and display metadata (last4, bank name).
func (h *Handlers) CreateBankAccount(w http.ResponseWriter, r *http.Request) {
	var req bankAccountRequest
	if !bind(w, r, &req) {
		return
	}
	if req.Processor != processor.Stripe && req.Processor != processor.Plaid {
		respond.ErrorParam(w, r, http.StatusBadRequest, respond.CodeValidationError, "processor must be 'stripe' or 'plaid'", "processor")
		return
	}
	if req.ProcessorAcctID == "" {
		respond.ErrorParam(w, r, http.StatusBadRequest, respond.CodeValidationError, "processor_acct_id is required", "processor_acct_id")
		return
	}
	// Currency is optional on a bank account, but when present it must be a
	// supported ISO 4217 code, normalized like every other money path.
	req.Currency = strings.ToUpper(req.Currency)
	if req.Currency != "" && !currency.ValidateCurrency(req.Currency) {
		respond.ErrorParam(w, r, http.StatusBadRequest, respond.CodeValidationError, "currency must be a supported ISO 4217 code", "currency")
		return
	}
	merchantID := middleware.MerchantID(r.Context())

	ba, err := h.bankAccounts.CreateBankAccount(r.Context(), store.NewBankAccount{
		MerchantID: merchantID, Processor: req.Processor, ProcessorAcctID: req.ProcessorAcctID,
		Last4: req.Last4, BankName: req.BankName, Currency: req.Currency, IsDefault: req.IsDefault,
	})
	if err != nil {
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}
	h.auditMutation(r, "bank_account.created", "bank_accounts", ba.ID)
	respond.JSON(w, r, http.StatusCreated, ba)
}

// ListBankAccounts lists the merchant's payout destinations.
func (h *Handlers) ListBankAccounts(w http.ResponseWriter, r *http.Request) {
	accts, err := h.bankAccounts.ListBankAccounts(r.Context(), middleware.MerchantID(r.Context()))
	if err != nil {
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}
	respond.JSON(w, r, http.StatusOK, listResponse[*models.BankAccount]{Data: accts})
}

// DeleteBankAccount soft-deletes a payout destination.
func (h *Handlers) DeleteBankAccount(w http.ResponseWriter, r *http.Request) {
	merchantID := middleware.MerchantID(r.Context())
	id := chi.URLParam(r, "id")
	if err := h.bankAccounts.SoftDeleteBankAccount(r.Context(), id, merchantID); err != nil {
		respondNotFoundOr500(w, r, err, store.ErrBankAccountNotFound, "bank account not found")
		return
	}
	h.auditMutation(r, "bank_account.deleted", "bank_accounts", id)
	w.WriteHeader(http.StatusNoContent)
}

// --- payouts ---------------------------------------------------------------

type payoutRequest struct {
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
	BankAccountID string `json:"bank_account_id"`
}

// CreatePayout initiates a payout to a bank account. The status starts pending
// and is later advanced by inbound processor webhooks.
func (h *Handlers) CreatePayout(w http.ResponseWriter, r *http.Request) {
	var req payoutRequest
	if !bind(w, r, &req) {
		return
	}
	// Normalize before validating so "usd" and "USD" persist identically.
	req.Currency = strings.ToUpper(req.Currency)
	if req.Amount <= 0 {
		respond.ErrorParam(w, r, http.StatusBadRequest, respond.CodeValidationError, "amount must be a positive integer", "amount")
		return
	}
	if !currency.ValidateCurrency(req.Currency) {
		respond.ErrorParam(w, r, http.StatusBadRequest, respond.CodeValidationError, "currency must be a supported ISO 4217 code", "currency")
		return
	}
	if req.BankAccountID == "" {
		respond.ErrorParam(w, r, http.StatusBadRequest, respond.CodeValidationError, "bank_account_id is required", "bank_account_id")
		return
	}
	merchantID := middleware.MerchantID(r.Context())
	mode := middleware.Mode(r.Context())

	bankAccounts, err := h.bankAccounts.ListBankAccounts(r.Context(), merchantID)
	if err != nil {
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}
	var account *models.BankAccount
	for _, ba := range bankAccounts {
		if ba.ID == req.BankAccountID {
			account = ba
			break
		}
	}
	if account == nil {
		respond.Error(w, r, http.StatusNotFound, respond.CodeNotFound, "bank account not found")
		return
	}

	result, err := h.processor.CreatePayout(r.Context(), processor.PayoutRequest{
		Processor: account.Processor, Amount: req.Amount, Currency: req.Currency,
		Mode: mode, ProcessorAcctID: account.ProcessorAcctID,
	})
	if err != nil {
		log.Ctx(r.Context()).Error().Err(err).Str("processor", account.Processor).
			Int64("amount", req.Amount).Str("currency", req.Currency).Str("mode", mode).
			Msg("payout creation failed at processor")
		respond.Error(w, r, http.StatusBadGateway, respond.CodeProcessorError, "payment processor error")
		return
	}

	payout, err := h.payouts.CreatePayout(r.Context(), store.NewPayout{
		MerchantID: merchantID, BankAccountID: req.BankAccountID, Amount: req.Amount, Currency: req.Currency,
		Status: result.Status, Processor: account.Processor, ProcessorPayoutID: result.ProcessorPayoutID,
		IdempotencyKey: r.Header.Get("Idempotency-Key"), Mode: mode, ArrivalDate: result.ArrivalDate,
	})
	if err != nil {
		log.Ctx(r.Context()).Error().Err(err).Msg("persisting payout failed after processor call")
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}
	// Money trail: bank details are never logged — only the opaque account id.
	log.Ctx(r.Context()).Info().Str("payout_id", payout.ID).Str("merchant_id", merchantID).
		Int64("amount", payout.Amount).Str("currency", payout.Currency).
		Str("processor", payout.Processor).Str("status", payout.Status).Str("mode", mode).
		Msg("payout created")
	h.auditMutation(r, "payout.created", "payouts", payout.ID)
	respond.JSON(w, r, http.StatusCreated, payout)
}

// ListPayouts returns a page of the merchant's payouts.
func (h *Handlers) ListPayouts(w http.ResponseWriter, r *http.Request) {
	payouts, next, err := h.payouts.ListPayouts(r.Context(),
		middleware.MerchantID(r.Context()), middleware.Mode(r.Context()),
		parseLimit(r.URL.Query().Get("limit")), r.URL.Query().Get("cursor"))
	if err != nil {
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}
	respond.JSON(w, r, http.StatusOK, listResponse[*models.Payout]{Data: payouts, NextCursor: next})
}

// GetPayout returns one payout scoped to merchant + mode.
func (h *Handlers) GetPayout(w http.ResponseWriter, r *http.Request) {
	payout, err := h.payouts.GetPayout(r.Context(), chi.URLParam(r, "id"),
		middleware.MerchantID(r.Context()), middleware.Mode(r.Context()))
	if err != nil {
		respondNotFoundOr500(w, r, err, store.ErrPayoutNotFound, "payout not found")
		return
	}
	respond.JSON(w, r, http.StatusOK, payout)
}
