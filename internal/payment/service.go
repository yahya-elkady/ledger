package payment

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/yahya-elkady/ledger/internal/ledger"
)

// System account IDs the orchestration posts against. These must match the
// accounts seeded by migration 0002 — they are the single source of truth
// shared by the migration's INSERT and the entry sets built below.
const (
	// AccountCashInTransit (asset) holds funds in flight: reserved on
	// authorize, recognized on capture, released on void.
	AccountCashInTransit = "sys-cash-in-transit"
	// AccountAuthorizationHold (liability) models the intermediate held
	// state — funds held for us but not yet earned.
	AccountAuthorizationHold = "sys-authorization-hold"
	// AccountSettledFunds (revenue) is where captured funds are recognized.
	AccountSettledFunds = "sys-settled-funds"
)

// ErrPaymentNotFound is returned by a PaymentRepository when no payment matches
// the given ID or idempotency key. The service treats a not-found idempotency
// lookup as "this is a new payment", so it relies on this sentinel.
var ErrPaymentNotFound = errors.New("payment not found")

// LedgerWriter is the slice of the ledger store the service needs: it writes a
// balanced EntrySet under a transaction ID. *store.LedgerStore satisfies it.
type LedgerWriter interface {
	Store(ctx context.Context, transactionID string, es ledger.EntrySet) error
}

// PaymentRepository persists and loads payments. *store.PaymentStore satisfies
// it; tests substitute an in-memory fake. A missing payment is reported as
// ErrPaymentNotFound so the service can distinguish "absent" from a real error.
type PaymentRepository interface {
	CreatePayment(ctx context.Context, p *Payment) error
	GetPayment(ctx context.Context, id string) (*Payment, error)
	GetPaymentByIdempotencyKey(ctx context.Context, key string) (*Payment, error)
	UpdatePaymentStatus(ctx context.Context, p *Payment) error
}

// Service orchestrates a payment's lifecycle: it drives the Phase 1 state
// machine and, at each transition, writes the matching balanced EntrySet
// through the ledger. The ledger stays dumb — every decision about which
// entries to write lives here.
//
// Dependencies are interfaces so the service can be exercised with the
// FakeProvider and an in-memory store, and run against real Postgres unchanged.
//
// Note on dual writes: a transition touches three systems in sequence — the
// provider, the ledger, and the payment row. A crash between any two leaves a
// gap (e.g. provider captured but ledger not yet written). Closing that gap
// (an outbox / reconciliation) is deliberately out of scope for this iteration;
// the ordering below is chosen so the *persisted* state is never ahead of the
// real-world money movement.
type Service struct {
	payments PaymentRepository
	ledger   LedgerWriter
	provider Provider
}

// NewService wires the service to its dependencies.
func NewService(payments PaymentRepository, ledgerWriter LedgerWriter, provider Provider) *Service {
	return &Service{
		payments: payments,
		ledger:   ledgerWriter,
		provider: provider,
	}
}

// CreatePayment creates a new pending payment, or returns the existing one if a
// payment with the same idempotency key already exists.
//
// Idempotency lives here, at creation: a repeated key short-circuits before any
// row is written, so a payment is born exactly once. No ledger entries are
// written by creation — money only moves on Authorize/Capture/Void.
func (s *Service) CreatePayment(ctx context.Context, id string, amount ledger.Money, idempotencyKey string) (*Payment, error) {
	existing, err := s.payments.GetPaymentByIdempotencyKey(ctx, idempotencyKey)
	switch {
	case err == nil:
		// Already created under this key: return it untouched, do no new work.
		return existing, nil
	case errors.Is(err, ErrPaymentNotFound):
		// Expected for a genuinely new key — fall through to create.
	default:
		return nil, fmt.Errorf("idempotency lookup: %w", err)
	}

	p, err := NewPayment(id, amount, idempotencyKey)
	if err != nil {
		return nil, err
	}
	if err := s.payments.CreatePayment(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// Authorize asks the provider to hold funds and, on success, records the hold
// in the ledger: debit cash-in-transit (asset), credit authorization-hold
// (liability). Legal only from pending.
//
//   - Provider declines  → payment moves to failed; no ledger entries.
//   - Provider errors     → payment left pending for retry; error surfaced.
//   - Provider authorizes → hold entries written, payment moves to authorized.
func (s *Service) Authorize(ctx context.Context, paymentID string) (*Payment, error) {
	p, err := s.payments.GetPayment(ctx, paymentID)
	if err != nil {
		return nil, err
	}

	// Reject an illegal authorize before calling the provider, so we never hold
	// funds for a payment that cannot legally advance.
	if p.Status != StatusPending {
		return nil, fmt.Errorf("%w: cannot authorize from %s", ErrInvalidTransition, p.Status)
	}

	ref, err := s.provider.Authorize(ctx, p.Amount)
	if err != nil {
		if errors.Is(err, ErrDeclined) {
			// A decline is a normal business outcome: move to failed, write no
			// hold entries, and report success — the payment is now terminal.
			if ferr := p.Fail(); ferr != nil {
				return nil, ferr
			}
			if uerr := s.payments.UpdatePaymentStatus(ctx, p); uerr != nil {
				return nil, uerr
			}
			return p, nil
		}
		// Unexpected provider/transport error: leave the payment pending so the
		// caller can retry. No ledger entries, no status change.
		return nil, fmt.Errorf("provider authorize: %w", err)
	}

	// Funds are held. Advance state in memory, then write the hold entries, then
	// persist — so the row is only marked authorized once the ledger agrees.
	if err := p.Authorize(ref); err != nil {
		return nil, err
	}
	if err := s.postEntries(ctx, p, "authorize", AccountCashInTransit, AccountAuthorizationHold, "authorization hold"); err != nil {
		return nil, err
	}
	if err := s.payments.UpdatePaymentStatus(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// Capture takes the previously held funds: debit authorization-hold, credit
// settled-funds. This clears the hold and recognizes the money. Legal only
// from authorized.
//
// On a provider error the persisted payment is left authorized (nothing was
// saved), so the caller may retry; no ledger entries are written.
func (s *Service) Capture(ctx context.Context, paymentID string) (*Payment, error) {
	return s.settleHold(ctx, paymentID, captureTransition,
		"capture", AccountAuthorizationHold, AccountSettledFunds, "capture settlement")
}

// Void releases the hold without taking funds: debit authorization-hold, credit
// cash-in-transit, returning the payment to a net-zero position. Legal only
// from authorized. Provider-error behaviour matches Capture.
func (s *Service) Void(ctx context.Context, paymentID string) (*Payment, error) {
	return s.settleHold(ctx, paymentID, voidTransition,
		"void", AccountAuthorizationHold, AccountCashInTransit, "void release")
}

// holdSettlement names the two transitions that resolve an authorization hold.
// Each pairs the in-memory state move with the corresponding provider call.
type holdSettlement struct {
	advance      func(*Payment) error
	callProvider func(context.Context, Provider, string) error
}

var (
	captureTransition = holdSettlement{
		advance: (*Payment).Capture,
		callProvider: func(ctx context.Context, pr Provider, ref string) error {
			return pr.Capture(ctx, ref)
		},
	}
	voidTransition = holdSettlement{
		advance: (*Payment).Void,
		callProvider: func(ctx context.Context, pr Provider, ref string) error {
			return pr.Void(ctx, ref)
		},
	}
)

// settleHold is the shared body of Capture and Void: both load the payment,
// validate the transition, call the provider, write a hold-clearing EntrySet,
// and persist. They differ only in which transition runs, which provider call
// fires, and which accounts the entries hit.
func (s *Service) settleHold(
	ctx context.Context,
	paymentID string,
	t holdSettlement,
	action, debitAccount, creditAccount, memo string,
) (*Payment, error) {
	p, err := s.payments.GetPayment(ctx, paymentID)
	if err != nil {
		return nil, err
	}

	// Validate the transition before touching the provider. This mutates only
	// the in-memory copy; nothing is persisted until the ledger write succeeds,
	// so an illegal move writes no entries and leaves the stored payment intact.
	if err := t.advance(p); err != nil {
		return nil, err
	}

	if err := t.callProvider(ctx, s.provider, p.ProviderRef); err != nil {
		// Provider failed: the persisted payment is still authorized (we never
		// saved the advance). Surface the error; the caller may retry.
		return nil, fmt.Errorf("provider %s: %w", action, err)
	}

	if err := s.postEntries(ctx, p, action, debitAccount, creditAccount, memo); err != nil {
		return nil, err
	}
	if err := s.payments.UpdatePaymentStatus(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// postEntries builds and writes the balanced two-leg EntrySet for one
// transition. All entries share a transaction ID derived from the payment ID
// and action, so they can be queried back together. The amount on both legs is
// the payment amount, so the set is balanced by construction.
func (s *Service) postEntries(ctx context.Context, p *Payment, action, debitAccount, creditAccount, memo string) error {
	txID := transactionID(p.ID, action)
	now := time.Now().UTC()

	entries := []ledger.Entry{
		{
			ID:        txID + ":debit",
			AccountID: debitAccount,
			Amount:    p.Amount,
			Direction: ledger.Debit,
			Memo:      memo,
			CreatedAt: now,
		},
		{
			ID:        txID + ":credit",
			AccountID: creditAccount,
			Amount:    p.Amount,
			Direction: ledger.Credit,
			Memo:      memo,
			CreatedAt: now,
		},
	}

	es, err := ledger.NewEntrySet(entries)
	if err != nil {
		return fmt.Errorf("building %s entry set: %w", action, err)
	}
	if err := s.ledger.Store(ctx, txID, es); err != nil {
		return fmt.Errorf("storing %s entries: %w", action, err)
	}
	return nil
}

// transactionID groups all entries for one transition of one payment. The
// deterministic shape (paymentID:action) makes the entries easy to query back.
func transactionID(paymentID, action string) string {
	return paymentID + ":" + action
}
