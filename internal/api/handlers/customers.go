package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/yahya-elkady/ledger/internal/api/middleware"
	"github.com/yahya-elkady/ledger/internal/api/respond"
	"github.com/yahya-elkady/ledger/internal/models"
	"github.com/yahya-elkady/ledger/internal/store"
)

// customerRequest is the create/update body for a customer.
type customerRequest struct {
	Email    string          `json:"email"`
	Name     string          `json:"name"`
	Metadata json.RawMessage `json:"metadata"`
}

// CreateCustomer creates a customer owned by the authenticated merchant.
func (h *Handlers) CreateCustomer(w http.ResponseWriter, r *http.Request) {
	var req customerRequest
	if !bind(w, r, &req) {
		return
	}
	merchantID := middleware.MerchantID(r.Context())

	cust, err := h.customers.CreateCustomer(r.Context(), merchantID, req.Email, req.Name, metadataBytes(req.Metadata))
	if err != nil {
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}

	actorType, actorID := auditActor(r.Context())
	h.writeAudit(r, store.AuditEntry{
		MerchantID: merchantID, ActorType: actorType, ActorID: actorID,
		Action: "customer.created", Resource: "customers", ResourceID: cust.ID, IP: clientIP(r),
	})

	respond.JSON(w, r, http.StatusCreated, cust)
}

// ListCustomers returns a cursor-paginated page of the merchant's customers.
func (h *Handlers) ListCustomers(w http.ResponseWriter, r *http.Request) {
	merchantID := middleware.MerchantID(r.Context())
	limit := parseLimit(r.URL.Query().Get("limit"))
	cursor := r.URL.Query().Get("cursor")

	custs, next, err := h.customers.ListCustomers(r.Context(), merchantID, limit, cursor)
	if err != nil {
		if errors.Is(err, store.ErrInvalidCursor) {
			respond.ErrorParam(w, r, http.StatusBadRequest, respond.CodeValidationError, "invalid cursor", "cursor")
			return
		}
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}

	respond.JSON(w, r, http.StatusOK, listResponse[*models.Customer]{Data: custs, NextCursor: next})
}

// GetCustomer returns one customer scoped to the merchant.
func (h *Handlers) GetCustomer(w http.ResponseWriter, r *http.Request) {
	merchantID := middleware.MerchantID(r.Context())
	cust, err := h.customers.GetCustomer(r.Context(), chi.URLParam(r, "id"), merchantID)
	if err != nil {
		respondNotFoundOr500(w, r, err, store.ErrCustomerNotFound, "customer not found")
		return
	}
	respond.JSON(w, r, http.StatusOK, cust)
}

// UpdateCustomer replaces a customer's mutable fields.
func (h *Handlers) UpdateCustomer(w http.ResponseWriter, r *http.Request) {
	var req customerRequest
	if !bind(w, r, &req) {
		return
	}
	merchantID := middleware.MerchantID(r.Context())

	cust, err := h.customers.UpdateCustomer(r.Context(), chi.URLParam(r, "id"), merchantID, req.Email, req.Name, metadataBytes(req.Metadata))
	if err != nil {
		respondNotFoundOr500(w, r, err, store.ErrCustomerNotFound, "customer not found")
		return
	}

	actorType, actorID := auditActor(r.Context())
	h.writeAudit(r, store.AuditEntry{
		MerchantID: merchantID, ActorType: actorType, ActorID: actorID,
		Action: "customer.updated", Resource: "customers", ResourceID: cust.ID, IP: clientIP(r),
	})

	respond.JSON(w, r, http.StatusOK, cust)
}

// DeleteCustomer soft-deletes a customer.
func (h *Handlers) DeleteCustomer(w http.ResponseWriter, r *http.Request) {
	merchantID := middleware.MerchantID(r.Context())
	id := chi.URLParam(r, "id")

	if err := h.customers.SoftDeleteCustomer(r.Context(), id, merchantID); err != nil {
		respondNotFoundOr500(w, r, err, store.ErrCustomerNotFound, "customer not found")
		return
	}

	actorType, actorID := auditActor(r.Context())
	h.writeAudit(r, store.AuditEntry{
		MerchantID: merchantID, ActorType: actorType, ActorID: actorID,
		Action: "customer.deleted", Resource: "customers", ResourceID: id, IP: clientIP(r),
	})

	w.WriteHeader(http.StatusNoContent)
}

// metadataBytes converts request metadata JSON into raw bytes for storage,
// treating empty/`null` as no metadata.
func metadataBytes(m json.RawMessage) []byte {
	if len(m) == 0 || string(m) == "null" {
		return nil
	}
	return m
}

// parseLimit parses the ?limit query param, falling back to the default and
// clamping at the maximum.
func parseLimit(s string) int {
	if s == "" {
		return store.DefaultPageSize
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return store.DefaultPageSize
	}
	if n > store.MaxPageSize {
		return store.MaxPageSize
	}
	return n
}
