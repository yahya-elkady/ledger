package handlers

import (
	"io"
	"net/http"

	"github.com/rs/zerolog/log"

	"github.com/yahya-elkady/ledger/internal/api/respond"
	"github.com/yahya-elkady/ledger/internal/models"
	"github.com/yahya-elkady/ledger/internal/store"
	"github.com/yahya-elkady/ledger/internal/webhook"
)

// maxWebhookBody caps an inbound webhook payload.
const maxWebhookBody = 1 << 20 // 1 MiB

// StripeWebhook receives inbound Stripe events. It verifies the Stripe-Signature
// header before trusting the payload, then advances the matching record.
//
// Webhook security: an event with a missing or invalid signature is never
// processed — verification happens before any state change.
func (h *Handlers) StripeWebhook(w http.ResponseWriter, r *http.Request) {
	h.handleInboundWebhook(w, r, h.stripeWebhook, r.Header.Get("Stripe-Signature"), "stripe")
}

// PlaidWebhook receives inbound Plaid events with the same verify-then-dispatch
// flow.
func (h *Handlers) PlaidWebhook(w http.ResponseWriter, r *http.Request) {
	h.handleInboundWebhook(w, r, h.plaidWebhook, r.Header.Get("Plaid-Verification"), "plaid")
}

// handleInboundWebhook verifies and dispatches one inbound webhook from the
// named processor.
func (h *Handlers) handleInboundWebhook(w http.ResponseWriter, r *http.Request, verifier webhook.Verifier, signature, source string) {
	if verifier == nil {
		respond.Error(w, r, http.StatusServiceUnavailable, respond.CodeInternalError, "webhooks not configured")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	if err != nil {
		respond.Error(w, r, http.StatusBadRequest, respond.CodeValidationError, "could not read request body")
		return
	}

	event, err := verifier.Verify(body, signature)
	if err != nil {
		// Invalid signature (or parse failure): reject without processing. A
		// failed verification on this endpoint is a potential forgery attempt.
		log.Ctx(r.Context()).Warn().Str("source", source).Str("ip", clientIP(r)).
			Bool("signature_present", signature != "").
			Msg("inbound webhook rejected: signature verification failed")
		respond.Error(w, r, http.StatusBadRequest, respond.CodeValidationError, "invalid webhook signature")
		return
	}

	h.dispatchWebhookEvent(r, event, source)
	// Acknowledge receipt; processors retry on non-2xx.
	w.WriteHeader(http.StatusOK)
}

// dispatchWebhookEvent applies a normalized event from the named processor to
// the matching record, audits the system-initiated mutation, then relays it to
// the owning merchant's webhook endpoints (Phase 7). A not-found target or
// unhandled kind is logged and ignored (still acked) so the processor does not
// retry indefinitely.
func (h *Handlers) dispatchWebhookEvent(r *http.Request, e webhook.Event, source string) {
	ctx := r.Context()
	var err error
	// The updated record carries the merchant id + mode the outbound relay
	// needs, and is also the event's payload (response models, never secrets).
	var merchantID, mode, eventType, resource, resourceID string
	var data any
	switch e.Kind {
	case webhook.ChargeSucceeded:
		var c *models.Charge
		if c, err = h.charges.UpdateStatusByProcessorID(ctx, e.ObjectID, "succeeded", "", ""); err == nil {
			merchantID, mode, eventType, data = c.MerchantID, c.Mode, "charge.succeeded", c
			resource, resourceID = "charges", c.ID
		}
	case webhook.ChargeFailed:
		var c *models.Charge
		if c, err = h.charges.UpdateStatusByProcessorID(ctx, e.ObjectID, "failed", e.FailureCode, e.FailureMessage); err == nil {
			merchantID, mode, eventType, data = c.MerchantID, c.Mode, "charge.failed", c
			resource, resourceID = "charges", c.ID
		}
	case webhook.PayoutPaid:
		var p *models.Payout
		if p, err = h.payouts.UpdateStatusByProcessorID(ctx, e.ObjectID, "paid", ""); err == nil {
			merchantID, mode, eventType, data = p.MerchantID, p.Mode, "payout.paid", p
			resource, resourceID = "payouts", p.ID
		}
	case webhook.PayoutFailed:
		var p *models.Payout
		if p, err = h.payouts.UpdateStatusByProcessorID(ctx, e.ObjectID, "failed", e.FailureMessage); err == nil {
			merchantID, mode, eventType, data = p.MerchantID, p.Mode, "payout.failed", p
			resource, resourceID = "payouts", p.ID
		}
	case webhook.SubscriptionCanceled:
		var s *models.Subscription
		if s, err = h.subscriptions.UpdateStatusByProcessorID(ctx, e.ObjectID, "canceled"); err == nil {
			merchantID, mode, eventType, data = s.MerchantID, s.Mode, "subscription.canceled", s
			resource, resourceID = "subscriptions", s.ID
		}
	case webhook.SubscriptionUpdated:
		var s *models.Subscription
		if s, err = h.subscriptions.UpdateStatusByProcessorID(ctx, e.ObjectID, e.Status); err == nil {
			merchantID, mode, eventType, data = s.MerchantID, s.Mode, "subscription.updated", s
			resource, resourceID = "subscriptions", s.ID
		}
	case webhook.Unhandled:
		log.Ctx(ctx).Debug().Str("source", source).Msg("ignoring unhandled webhook event")
		return
	}
	if err != nil {
		log.Ctx(ctx).Warn().Err(err).Str("source", source).Int("kind", int(e.Kind)).
			Str("object_id", e.ObjectID).Msg("webhook event could not be applied")
		return
	}

	// Webhook-driven state changes are mutations like any other: audit them with
	// a system actor so the trail shows the processor (not a person) acted.
	h.writeAudit(r, store.AuditEntry{
		MerchantID: merchantID,
		ActorType:  "system",
		ActorID:    "webhook:" + source,
		Action:     eventType,
		Resource:   resource,
		ResourceID: resourceID,
		IP:         clientIP(r),
	})
	h.emitEvent(ctx, merchantID, mode, eventType, data)
}
