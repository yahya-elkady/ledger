package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/yahya-elkady/ledger/internal/api/middleware"
	"github.com/yahya-elkady/ledger/internal/api/respond"
	"github.com/yahya-elkady/ledger/internal/currency"
	"github.com/yahya-elkady/ledger/internal/metrics"
	"github.com/yahya-elkady/ledger/internal/models"
	"github.com/yahya-elkady/ledger/internal/processor"
	"github.com/yahya-elkady/ledger/internal/store"
)

// chargeRequest is the POST /v1/charges body.
type chargeRequest struct {
	Amount          int64           `json:"amount"`
	Currency        string          `json:"currency"`
	CustomerID      string          `json:"customer_id"`
	PaymentMethodID string          `json:"payment_method_id"`
	Processor       string          `json:"processor"`
	Description     string          `json:"description"`
	Metadata        json.RawMessage `json:"metadata"`
}

// refundRequest is the POST /v1/charges/:id/refund body.
type refundRequest struct {
	Amount int64 `json:"amount"` // 0 => full remaining amount
}

// CreateCharge takes a one-time payment: validate → call processor → persist the
// result → audit → return.
//
// PCI-DSS: card data never touches this service; the processor is given a
// tokenized payment method id and returns only a reference id and status. The
// amount is an integer (cents); no floating-point money is used anywhere.
func (h *Handlers) CreateCharge(w http.ResponseWriter, r *http.Request) {
	var req chargeRequest
	if !bind(w, r, &req) {
		return
	}
	if msg, param, ok := validateCharge(req); !ok {
		respond.ErrorParam(w, r, http.StatusBadRequest, respond.CodeValidationError, msg, param)
		return
	}

	merchantID := middleware.MerchantID(r.Context())
	mode := middleware.Mode(r.Context())

	// Ask the processor to take the payment.
	result, err := h.processor.CreateCharge(r.Context(), processor.ChargeRequest{
		Processor:         req.Processor,
		Amount:            req.Amount,
		Currency:          req.Currency,
		Mode:              mode,
		ProcessorMethodID: req.PaymentMethodID,
		Description:       req.Description,
	})
	if err != nil {
		respond.Error(w, r, http.StatusBadGateway, respond.CodeProcessorError, "payment processor error")
		return
	}

	charge, err := h.charges.CreateCharge(r.Context(), store.NewCharge{
		MerchantID:        merchantID,
		CustomerID:        req.CustomerID,
		PaymentMethodID:   req.PaymentMethodID,
		Amount:            req.Amount,
		Currency:          req.Currency,
		Status:            result.Status,
		Processor:         req.Processor,
		ProcessorChargeID: result.ProcessorChargeID,
		IdempotencyKey:    r.Header.Get("Idempotency-Key"),
		Mode:              mode,
		FailureCode:       result.FailureCode,
		FailureMessage:    result.FailureMessage,
		Metadata:          metadataBytes(req.Metadata),
	})
	if err != nil {
		if errors.Is(err, store.ErrIdempotencyConflict) {
			respond.Error(w, r, http.StatusConflict, respond.CodeIdempotencyConflict, "a charge with this idempotency key already exists")
			return
		}
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}

	h.auditMutation(r, "charge.created", "charges", charge.ID)
	metrics.Charge(charge.Status, charge.Currency, charge.Processor, charge.Mode, charge.Amount)
	// Notify the merchant's webhook endpoints of the synchronous outcome.
	event := "charge.succeeded"
	if charge.Status == "failed" {
		event = "charge.failed"
	}
	h.emitEvent(r.Context(), merchantID, mode, event, charge)
	respond.JSON(w, r, http.StatusCreated, charge)
}

// ListCharges returns a mode-isolated, merchant-scoped page of charges, optionally
// filtered by ?status=.
func (h *Handlers) ListCharges(w http.ResponseWriter, r *http.Request) {
	merchantID := middleware.MerchantID(r.Context())
	mode := middleware.Mode(r.Context())

	charges, next, err := h.charges.ListCharges(r.Context(), merchantID, mode, parseChargeFilter(r))
	if err != nil {
		if errors.Is(err, store.ErrInvalidCursor) {
			respond.ErrorParam(w, r, http.StatusBadRequest, respond.CodeValidationError, "invalid cursor", "cursor")
			return
		}
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}
	respond.JSON(w, r, http.StatusOK, listResponse[*models.Charge]{Data: charges, NextCursor: next})
}

// GetCharge returns one charge scoped to merchant + mode.
func (h *Handlers) GetCharge(w http.ResponseWriter, r *http.Request) {
	charge, err := h.charges.GetCharge(r.Context(), chi.URLParam(r, "id"),
		middleware.MerchantID(r.Context()), middleware.Mode(r.Context()))
	if err != nil {
		respondNotFoundOr500(w, r, err, store.ErrChargeNotFound, "charge not found")
		return
	}
	respond.JSON(w, r, http.StatusOK, charge)
}

// RefundCharge issues a full or partial refund against a charge.
//
// PCI-DSS: the refund is executed by the processor against a stored reference
// id; no card data is involved.
func (h *Handlers) RefundCharge(w http.ResponseWriter, r *http.Request) {
	var req refundRequest
	if !bind(w, r, &req) {
		return
	}
	merchantID := middleware.MerchantID(r.Context())
	mode := middleware.Mode(r.Context())

	charge, err := h.charges.GetCharge(r.Context(), chi.URLParam(r, "id"), merchantID, mode)
	if err != nil {
		respondNotFoundOr500(w, r, err, store.ErrChargeNotFound, "charge not found")
		return
	}
	if charge.Status != "succeeded" && charge.Status != "partially_refunded" {
		respond.Error(w, r, http.StatusBadRequest, respond.CodeValidationError, "only succeeded charges can be refunded")
		return
	}

	// Default to the full remaining amount; validate partial refunds.
	remaining := charge.Amount - charge.RefundedAmount
	amount := req.Amount
	if amount == 0 {
		amount = remaining
	}
	if amount <= 0 || amount > remaining {
		respond.ErrorParam(w, r, http.StatusBadRequest, respond.CodeValidationError,
			"refund amount must be positive and not exceed the unrefunded amount", "amount")
		return
	}

	if _, err := h.processor.RefundCharge(r.Context(), charge.Processor, charge.ProcessorChargeID, amount, mode); err != nil {
		respond.Error(w, r, http.StatusBadGateway, respond.CodeProcessorError, "payment processor error")
		return
	}

	newRefunded := charge.RefundedAmount + amount
	status := "partially_refunded"
	if newRefunded == charge.Amount {
		status = "refunded"
	}
	updated, err := h.charges.SetRefund(r.Context(), charge.ID, merchantID, newRefunded, status)
	if err != nil {
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}

	h.auditMutation(r, "charge.refunded", "charges", updated.ID)
	h.emitEvent(r.Context(), merchantID, mode, "charge.refunded", updated)
	respond.JSON(w, r, http.StatusOK, updated)
}

// parseChargeFilter builds a ChargeFilter from the request query params.
func parseChargeFilter(r *http.Request) store.ChargeFilter {
	return store.ChargeFilter{
		Status: r.URL.Query().Get("status"),
		Limit:  parseLimit(r.URL.Query().Get("limit")),
		Cursor: r.URL.Query().Get("cursor"),
	}
}

// validateCharge validates the create-charge request.
func validateCharge(req chargeRequest) (string, string, bool) {
	if req.Amount <= 0 {
		return "amount must be a positive integer (minor units)", "amount", false
	}
	if !currency.ValidateCurrency(req.Currency) {
		return "currency must be a supported ISO 4217 code", "currency", false
	}
	if req.CustomerID == "" && req.PaymentMethodID == "" {
		return "one of customer_id or payment_method_id is required", "payment_method_id", false
	}
	if req.Processor != processor.Stripe && req.Processor != processor.Plaid {
		return "processor must be 'stripe' or 'plaid'", "processor", false
	}
	return "", "", true
}
