package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/yahya-elkady/ledger/internal/metrics"
)

// Delivery statuses persisted in webhook_deliveries.status.
const (
	StatusPending   = "pending"   // queued or awaiting retry
	StatusDelivered = "delivered" // endpoint returned 2xx
	StatusFailed    = "failed"    // attempts exhausted; dead-lettered
)

// Endpoint is a merchant-registered outbound webhook destination, as the
// dispatcher needs it (URL only — the signing secret is derived, never loaded).
type Endpoint struct {
	ID  string
	URL string
}

// Delivery is one pending webhook delivery joined with its endpoint's URL.
type Delivery struct {
	ID           string
	EndpointID   string
	EventType    string
	Payload      []byte // the event's "data" object, raw JSON
	AttemptCount int    // attempts already made (0 for a fresh delivery)
	URL          string
}

// AttemptOutcome records the result of one delivery attempt.
type AttemptOutcome struct {
	Status         string     // StatusDelivered, StatusPending (retry), or StatusFailed
	NextRetryAt    *time.Time // set only when Status is StatusPending
	ResponseStatus int        // last HTTP status (0 on transport error)
	ResponseBody   string     // truncated response body, for debugging
}

// DeliveryStore is the persistence the dispatcher needs. The sqlc-backed
// store.WebhookStore satisfies it in production; tests use an in-memory fake.
type DeliveryStore interface {
	// ActiveEndpoints returns the merchant's active endpoints (matching mode)
	// that subscribe to eventType.
	ActiveEndpoints(ctx context.Context, merchantID, mode, eventType string) ([]Endpoint, error)
	// CreateDelivery inserts a pending delivery row and returns its id.
	CreateDelivery(ctx context.Context, endpointID, eventType string, payload []byte) (string, error)
	// DueDeliveries returns pending deliveries whose next_retry_at has passed.
	DueDeliveries(ctx context.Context, limit int) ([]Delivery, error)
	// FinishAttempt records the outcome of one attempt (increments the count).
	FinishAttempt(ctx context.Context, deliveryID string, outcome AttemptOutcome) error
}

// DispatcherConfig carries the dispatcher's tunables, sourced from config
// (WEBHOOK_SIGNING_SECRET, WEBHOOK_DELIVERY_RETRIES, WEBHOOK_RETRY_BACKOFF_SECONDS).
type DispatcherConfig struct {
	SigningSecret string        // master secret; per-endpoint secrets are derived from it
	MaxAttempts   int           // total attempts before dead-lettering (default 5)
	BaseBackoff   time.Duration // backoff base: delay = base * 2^attempt (default 60s)
	PollInterval  time.Duration // how often the background loop scans for due rows (default 30s)
	Timeout       time.Duration // per-request HTTP timeout (default 30s)
}

// Dispatcher delivers signed webhook events to merchant endpoints with
// exponential-backoff retries. Dispatch only persists pending rows (it never
// blocks the request path on merchant HTTP); the Run loop performs the actual
// deliveries in the background.
type Dispatcher struct {
	store  DeliveryStore
	client *http.Client
	cfg    DispatcherConfig
	wake   chan struct{} // nudges Run so fresh dispatches don't wait a full poll
}

// NewDispatcher constructs a Dispatcher, applying defaults for unset tunables.
func NewDispatcher(store DeliveryStore, cfg DispatcherConfig) *Dispatcher {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.BaseBackoff <= 0 {
		cfg.BaseBackoff = 60 * time.Second
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 30 * time.Second
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &Dispatcher{
		store:  store,
		client: &http.Client{Timeout: cfg.Timeout},
		cfg:    cfg,
		wake:   make(chan struct{}, 1),
	}
}

// Dispatch fans an event out to every active endpoint subscribed to eventType
// for the merchant + mode, persisting one pending delivery per endpoint. It
// performs no HTTP itself — the background loop delivers — so it is safe to
// call inline from request handlers. data is marshaled as the event's "data"
// object (models only; never secrets or card data).
func (d *Dispatcher) Dispatch(ctx context.Context, merchantID, mode, eventType string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshaling webhook payload for %s: %w", eventType, err)
	}
	endpoints, err := d.store.ActiveEndpoints(ctx, merchantID, mode, eventType)
	if err != nil {
		return fmt.Errorf("listing webhook endpoints: %w", err)
	}
	for _, ep := range endpoints {
		if _, err := d.store.CreateDelivery(ctx, ep.ID, eventType, payload); err != nil {
			return fmt.Errorf("queueing webhook delivery to endpoint %s: %w", ep.ID, err)
		}
	}
	if len(endpoints) > 0 {
		// Non-blocking nudge: if the loop is mid-cycle the buffered slot is
		// already set and this send is dropped, which is fine.
		select {
		case d.wake <- struct{}{}:
		default:
		}
	}
	return nil
}

// Run polls for due deliveries every PollInterval (or sooner when Dispatch
// nudges it) and attempts them, until ctx is cancelled. Start it as a
// background goroutine from main; it never blocks the request path.
func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.PollInterval)
	defer ticker.Stop()
	log.Ctx(ctx).Info().Dur("poll_interval", d.cfg.PollInterval).Msg("webhook dispatcher started")
	for {
		if err := d.ProcessDue(ctx); err != nil && ctx.Err() == nil {
			log.Ctx(ctx).Warn().Err(err).Msg("webhook delivery sweep failed")
		}
		select {
		case <-ctx.Done():
			log.Ctx(ctx).Info().Msg("webhook dispatcher stopped")
			return
		case <-ticker.C:
		case <-d.wake:
		}
	}
}

// deliveryBatch caps how many due deliveries one sweep attempts.
const deliveryBatch = 50

// ProcessDue attempts every currently-due pending delivery once. Exported so
// tests (and operational tooling) can drive a sweep synchronously.
func (d *Dispatcher) ProcessDue(ctx context.Context) error {
	due, err := d.store.DueDeliveries(ctx, deliveryBatch)
	if err != nil {
		return fmt.Errorf("fetching due webhook deliveries: %w", err)
	}
	for _, delivery := range due {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		d.deliver(ctx, delivery)
	}
	return nil
}

// maxStoredResponseBody caps how much of the endpoint's response is persisted.
const maxStoredResponseBody = 1 << 10 // 1 KiB

// deliver makes one signed delivery attempt and records its outcome: 2xx marks
// the row delivered; anything else schedules a retry at base * 2^attempt, or
// dead-letters the row once MaxAttempts is reached. The signing secret is
// derived per endpoint and never logged or persisted.
func (d *Dispatcher) deliver(ctx context.Context, delivery Delivery) {
	now := time.Now().UTC()
	body, err := json.Marshal(map[string]any{
		"id":      delivery.ID,
		"type":    delivery.EventType,
		"created": now.Unix(),
		"data":    json.RawMessage(delivery.Payload),
	})
	if err != nil {
		// Corrupt stored payload: dead-letter immediately, a retry cannot fix it.
		d.finish(ctx, delivery.ID, AttemptOutcome{Status: StatusFailed, ResponseBody: "invalid stored payload"})
		metrics.WebhookDelivery("failed")
		return
	}

	status, respBody, err := d.post(ctx, delivery, body, now.Unix())
	if err == nil && status >= 200 && status < 300 {
		d.finish(ctx, delivery.ID, AttemptOutcome{Status: StatusDelivered, ResponseStatus: status, ResponseBody: respBody})
		metrics.WebhookDelivery("delivered")
		return
	}
	if err != nil {
		respBody = err.Error() // transport error; no HTTP response to record
	}

	attempt := delivery.AttemptCount + 1 // count including this attempt
	if attempt >= d.cfg.MaxAttempts {
		d.finish(ctx, delivery.ID, AttemptOutcome{Status: StatusFailed, ResponseStatus: status, ResponseBody: respBody})
		metrics.WebhookDelivery("failed")
		log.Ctx(ctx).Warn().Str("delivery_id", delivery.ID).Str("event", delivery.EventType).
			Int("attempts", attempt).Msg("webhook delivery dead-lettered")
		return
	}
	// Exponential backoff per build.md: base * 2^attempt.
	next := now.Add(d.cfg.BaseBackoff * (1 << attempt))
	log.Ctx(ctx).Warn().Str("delivery_id", delivery.ID).Str("event", delivery.EventType).
		Int("attempt", attempt).Int("response_status", status).Time("next_retry_at", next).
		Msg("webhook delivery attempt failed, retry scheduled")
	d.finish(ctx, delivery.ID, AttemptOutcome{Status: StatusPending, NextRetryAt: &next, ResponseStatus: status, ResponseBody: respBody})
}

// post performs the signed HTTP POST and returns the response status + body.
func (d *Dispatcher) post(ctx context.Context, delivery Delivery, body []byte, timestamp int64) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, delivery.URL, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	secret := DeriveEndpointSecret(d.cfg.SigningSecret, delivery.EndpointID)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Payments-Signature", "sha256="+SignPayload(body, secret, timestamp))
	req.Header.Set("X-Payments-Timestamp", fmt.Sprintf("%d", timestamp))

	resp, err := d.client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxStoredResponseBody))
	return resp.StatusCode, string(respBody), nil
}

// finish records an attempt outcome; a failed write is logged (the row will be
// retried on a later sweep) but never panics the loop.
func (d *Dispatcher) finish(ctx context.Context, deliveryID string, outcome AttemptOutcome) {
	if err := d.store.FinishAttempt(ctx, deliveryID, outcome); err != nil {
		log.Ctx(ctx).Error().Err(err).Str("delivery_id", deliveryID).Msg("recording webhook attempt failed")
	}
}
