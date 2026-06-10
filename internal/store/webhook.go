package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/yahya-elkady/ledger/internal/store/db"
	"github.com/yahya-elkady/ledger/internal/webhook"
)

// WebhookStore is the sqlc-backed persistence for outbound webhook deliveries.
// It satisfies webhook.DeliveryStore for the background dispatcher. Signing
// secrets are never stored here — the dispatcher derives them per endpoint.
type WebhookStore struct {
	queries *db.Queries
}

// Compile-time check: the store satisfies the dispatcher's interface.
var _ webhook.DeliveryStore = (*WebhookStore)(nil)

// NewWebhookStore constructs a WebhookStore from an open pool.
func NewWebhookStore(pool *pgxpool.Pool) *WebhookStore {
	return &WebhookStore{queries: db.New(pool)}
}

// ActiveEndpoints returns the merchant's active, non-deleted endpoints (in the
// given mode) whose subscription list contains eventType.
func (s *WebhookStore) ActiveEndpoints(ctx context.Context, merchantID, mode, eventType string) ([]webhook.Endpoint, error) {
	mid, err := textToUUID(merchantID)
	if err != nil {
		return nil, fmt.Errorf("invalid merchant id: %w", err)
	}
	rows, err := s.queries.ListActiveWebhookEndpoints(ctx, db.ListActiveWebhookEndpointsParams{
		MerchantID: mid,
		Mode:       mode,
		EventType:  eventType,
	})
	if err != nil {
		return nil, fmt.Errorf("listing webhook endpoints: %w", err)
	}
	endpoints := make([]webhook.Endpoint, 0, len(rows))
	for _, r := range rows {
		endpoints = append(endpoints, webhook.Endpoint{ID: uuidToText(r.ID), URL: r.Url})
	}
	return endpoints, nil
}

// CreateDelivery inserts one pending delivery row, due immediately.
func (s *WebhookStore) CreateDelivery(ctx context.Context, endpointID, eventType string, payload []byte) (string, error) {
	eid, err := textToUUID(endpointID)
	if err != nil {
		return "", fmt.Errorf("invalid endpoint id: %w", err)
	}
	id, err := s.queries.CreateWebhookDelivery(ctx, db.CreateWebhookDeliveryParams{
		EndpointID: eid,
		EventType:  eventType,
		Payload:    payload,
	})
	if err != nil {
		return "", fmt.Errorf("creating webhook delivery: %w", err)
	}
	return uuidToText(id), nil
}

// DueDeliveries returns pending deliveries whose retry time has passed, oldest
// first, joined with their endpoint URL.
func (s *WebhookStore) DueDeliveries(ctx context.Context, limit int) ([]webhook.Delivery, error) {
	rows, err := s.queries.DueWebhookDeliveries(ctx, int32(clampLimit(limit)))
	if err != nil {
		return nil, fmt.Errorf("listing due webhook deliveries: %w", err)
	}
	due := make([]webhook.Delivery, 0, len(rows))
	for _, r := range rows {
		due = append(due, webhook.Delivery{
			ID:           uuidToText(r.ID),
			EndpointID:   uuidToText(r.EndpointID),
			EventType:    r.EventType,
			Payload:      r.Payload,
			AttemptCount: int(r.AttemptCount),
			URL:          r.Url,
		})
	}
	return due, nil
}

// FinishAttempt records one attempt's outcome: bumps attempt_count, sets the
// new status, and stores the (truncated) response for debugging.
func (s *WebhookStore) FinishAttempt(ctx context.Context, deliveryID string, o webhook.AttemptOutcome) error {
	id, err := textToUUID(deliveryID)
	if err != nil {
		return fmt.Errorf("invalid delivery id: %w", err)
	}
	var nextRetry pgtype.Timestamptz
	if o.NextRetryAt != nil {
		nextRetry = timeToTS(*o.NextRetryAt)
	}
	var respStatus *int32
	if o.ResponseStatus != 0 {
		v := int32(o.ResponseStatus)
		respStatus = &v
	}
	var respBody *string
	if o.ResponseBody != "" {
		respBody = &o.ResponseBody
	}
	if err := s.queries.FinishWebhookDeliveryAttempt(ctx, db.FinishWebhookDeliveryAttemptParams{
		ID:                 id,
		Status:             o.Status,
		NextRetryAt:        nextRetry,
		LastResponseStatus: respStatus,
		LastResponseBody:   respBody,
	}); err != nil {
		return fmt.Errorf("recording webhook attempt: %w", err)
	}
	return nil
}
