package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/yahya-elkady/ledger/internal/api/middleware"
	"github.com/yahya-elkady/ledger/internal/api/respond"
	"github.com/yahya-elkady/ledger/internal/currency"
	"github.com/yahya-elkady/ledger/internal/models"
	"github.com/yahya-elkady/ledger/internal/processor"
	"github.com/yahya-elkady/ledger/internal/store"
)

// --- plans -----------------------------------------------------------------

type planRequest struct {
	Name          string `json:"name"`
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
	Interval      string `json:"interval"`
	IntervalCount int    `json:"interval_count"`
}

var validIntervals = map[string]bool{"day": true, "week": true, "month": true, "year": true}

// CreatePlan creates a recurring price/plan, registering it with the processor.
func (h *Handlers) CreatePlan(w http.ResponseWriter, r *http.Request) {
	var req planRequest
	if !bind(w, r, &req) {
		return
	}
	if msg, param, ok := validatePlan(req); !ok {
		respond.ErrorParam(w, r, http.StatusBadRequest, respond.CodeValidationError, msg, param)
		return
	}
	merchantID := middleware.MerchantID(r.Context())
	mode := middleware.Mode(r.Context())
	count := req.IntervalCount
	if count == 0 {
		count = 1
	}

	processorPlanID, err := h.processor.CreatePlan(r.Context(), processor.PlanRequest{
		Name: req.Name, Amount: req.Amount, Currency: req.Currency,
		Interval: req.Interval, IntervalCount: count, Mode: mode,
	})
	if err != nil {
		respond.Error(w, r, http.StatusBadGateway, respond.CodeProcessorError, "payment processor error")
		return
	}

	plan, err := h.plans.CreatePlan(r.Context(), store.NewPlan{
		MerchantID: merchantID, Name: req.Name, Amount: req.Amount, Currency: req.Currency,
		Interval: req.Interval, IntervalCount: count, ProcessorPlanID: processorPlanID, Mode: mode,
	})
	if err != nil {
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}
	h.auditMutation(r, "plan.created", "plans", plan.ID)
	respond.JSON(w, r, http.StatusCreated, plan)
}

// ListPlans returns the merchant's active plans for the current mode.
func (h *Handlers) ListPlans(w http.ResponseWriter, r *http.Request) {
	plans, err := h.plans.ListPlans(r.Context(), middleware.MerchantID(r.Context()), middleware.Mode(r.Context()))
	if err != nil {
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}
	respond.JSON(w, r, http.StatusOK, listResponse[*models.Plan]{Data: plans})
}

// DeletePlan soft-deletes a plan.
func (h *Handlers) DeletePlan(w http.ResponseWriter, r *http.Request) {
	merchantID := middleware.MerchantID(r.Context())
	id := chi.URLParam(r, "id")
	if err := h.plans.SoftDeletePlan(r.Context(), id, merchantID); err != nil {
		respondNotFoundOr500(w, r, err, store.ErrPlanNotFound, "plan not found")
		return
	}
	h.auditMutation(r, "plan.deleted", "plans", id)
	w.WriteHeader(http.StatusNoContent)
}

// --- subscriptions ---------------------------------------------------------

type subscriptionRequest struct {
	CustomerID      string          `json:"customer_id"`
	PlanID          string          `json:"plan_id"`
	PaymentMethodID string          `json:"payment_method_id"`
	TrialEnd        *time.Time      `json:"trial_end"`
	Metadata        json.RawMessage `json:"metadata"`
}

type cancelRequest struct {
	AtPeriodEnd bool `json:"at_period_end"`
}

// CreateSubscription starts a subscription for a customer on a plan.
func (h *Handlers) CreateSubscription(w http.ResponseWriter, r *http.Request) {
	var req subscriptionRequest
	if !bind(w, r, &req) {
		return
	}
	if req.CustomerID == "" || req.PlanID == "" || req.PaymentMethodID == "" {
		respond.Error(w, r, http.StatusBadRequest, respond.CodeValidationError,
			"customer_id, plan_id and payment_method_id are required")
		return
	}
	merchantID := middleware.MerchantID(r.Context())
	mode := middleware.Mode(r.Context())

	plan, err := h.plans.GetPlan(r.Context(), req.PlanID, merchantID, mode)
	if err != nil {
		respondNotFoundOr500(w, r, err, store.ErrPlanNotFound, "plan not found")
		return
	}

	var trialEnd time.Time
	if req.TrialEnd != nil {
		trialEnd = *req.TrialEnd
	}
	result, err := h.processor.CreateSubscription(r.Context(), processor.SubscriptionRequest{
		ProcessorPlanID: plan.ProcessorPlanID, ProcessorMethodID: req.PaymentMethodID, Mode: mode, TrialEnd: trialEnd,
	})
	if err != nil {
		respond.Error(w, r, http.StatusBadGateway, respond.CodeProcessorError, "payment processor error")
		return
	}

	sub, err := h.subscriptions.CreateSubscription(r.Context(), store.NewSubscription{
		MerchantID: merchantID, CustomerID: req.CustomerID, PlanID: req.PlanID, PaymentMethodID: req.PaymentMethodID,
		Status: result.Status, ProcessorSubID: result.ProcessorSubID,
		CurrentPeriodStart: result.CurrentPeriodStart, CurrentPeriodEnd: result.CurrentPeriodEnd,
		TrialEnd: trialEnd, Mode: mode, Metadata: metadataBytes(req.Metadata),
	})
	if err != nil {
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}
	h.auditMutation(r, "subscription.created", "subscriptions", sub.ID)
	respond.JSON(w, r, http.StatusCreated, sub)
}

// ListSubscriptions returns a page of subscriptions, optionally filtered by status.
func (h *Handlers) ListSubscriptions(w http.ResponseWriter, r *http.Request) {
	subs, next, err := h.subscriptions.ListSubscriptions(r.Context(),
		middleware.MerchantID(r.Context()), middleware.Mode(r.Context()), store.SubscriptionFilter{
			Status: r.URL.Query().Get("status"),
			Limit:  parseLimit(r.URL.Query().Get("limit")),
			Cursor: r.URL.Query().Get("cursor"),
		})
	if err != nil {
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}
	respond.JSON(w, r, http.StatusOK, listResponse[*models.Subscription]{Data: subs, NextCursor: next})
}

// GetSubscription returns one subscription scoped to merchant + mode.
func (h *Handlers) GetSubscription(w http.ResponseWriter, r *http.Request) {
	sub, err := h.subscriptions.GetSubscription(r.Context(), chi.URLParam(r, "id"),
		middleware.MerchantID(r.Context()), middleware.Mode(r.Context()))
	if err != nil {
		respondNotFoundOr500(w, r, err, store.ErrSubscriptionNotFound, "subscription not found")
		return
	}
	respond.JSON(w, r, http.StatusOK, sub)
}

// CancelSubscription cancels a subscription, immediately or at period end.
func (h *Handlers) CancelSubscription(w http.ResponseWriter, r *http.Request) {
	var req cancelRequest
	if r.ContentLength != 0 && !bind(w, r, &req) {
		return
	}
	merchantID := middleware.MerchantID(r.Context())
	mode := middleware.Mode(r.Context())

	sub, err := h.subscriptions.GetSubscription(r.Context(), chi.URLParam(r, "id"), merchantID, mode)
	if err != nil {
		respondNotFoundOr500(w, r, err, store.ErrSubscriptionNotFound, "subscription not found")
		return
	}

	if err := h.processor.CancelSubscription(r.Context(), sub.ProcessorSubID, req.AtPeriodEnd, mode); err != nil {
		respond.Error(w, r, http.StatusBadGateway, respond.CodeProcessorError, "payment processor error")
		return
	}

	// At-period-end stays active until the period closes; immediate cancellation
	// transitions to canceled now.
	status := "canceled"
	if req.AtPeriodEnd {
		status = sub.Status
	}
	updated, err := h.subscriptions.SetSubscriptionStatus(r.Context(), sub.ID, merchantID, status, !req.AtPeriodEnd)
	if err != nil {
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}
	h.auditMutation(r, "subscription.canceled", "subscriptions", updated.ID)
	respond.JSON(w, r, http.StatusOK, updated)
}

func validatePlan(req planRequest) (string, string, bool) {
	if req.Name == "" {
		return "name is required", "name", false
	}
	if req.Amount <= 0 {
		return "amount must be a positive integer (minor units)", "amount", false
	}
	if !currency.ValidateCurrency(req.Currency) {
		return "currency must be a supported ISO 4217 code", "currency", false
	}
	if !validIntervals[req.Interval] {
		return "interval must be day, week, month, or year", "interval", false
	}
	return "", "", true
}
