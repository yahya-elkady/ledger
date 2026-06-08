// Package payment owns the concept of a payment — its identity, its lifecycle,
// and (in Phase 2) the decision of which ledger entries to write at each stage.
//
// This file holds the pure, I/O-free core: the Payment entity, the Status
// lifecycle, and the transition rules that enforce the two-step
// authorize-then-capture flow. It writes no ledger entries and touches no
// database — that orchestration lives in service.go (Phase 2).
package payment

import (
	"errors"
	"fmt"
	"time"

	"github.com/yahya-elkady/ledger/internal/ledger"
)

// Status is the lifecycle state of a Payment.
//
// The legal flow is:
//
//	pending ──authorize──▶ authorized ──capture──▶ captured
//	   │                        │
//	   │                        └──────void───────▶ voided
//	   │
//	   └──(authorization fails)─▶ failed
//
// captured, voided, and failed are terminal: no further transition is legal.
type Status string

const (
	// StatusPending means the payment has been created but no money action
	// has been taken yet.
	StatusPending Status = "pending"
	// StatusAuthorized means the provider has confirmed and held the funds.
	StatusAuthorized Status = "authorized"
	// StatusCaptured means the funds have been taken; terminal success state.
	StatusCaptured Status = "captured"
	// StatusVoided means an authorization was released before capture; terminal.
	StatusVoided Status = "voided"
	// StatusFailed means authorization did not succeed; terminal.
	StatusFailed Status = "failed"
)

// action is an internal label for a state-machine edge. It is unexported
// because callers drive transitions through the named methods (Authorize,
// Capture, Void, Fail) rather than by naming actions directly.
type action string

const (
	actionAuthorize action = "authorize"
	actionCapture   action = "capture"
	actionVoid      action = "void"
	actionFail      action = "fail"
)

// transitions is the single source of truth for the state machine.
// It maps a (current status, action) pair to the resulting status. Any pair
// absent from this table is an illegal transition and is rejected.
var transitions = map[Status]map[action]Status{
	StatusPending: {
		actionAuthorize: StatusAuthorized,
		actionFail:      StatusFailed,
	},
	StatusAuthorized: {
		actionCapture: StatusCaptured,
		actionVoid:    StatusVoided,
	},
	// captured, voided, and failed are terminal: no outgoing edges.
}

// ErrInvalidTransition is returned when an action is attempted from a status
// that does not permit it (e.g. capturing a pending payment, voiding a
// captured one).
var ErrInvalidTransition = errors.New("invalid payment transition")

// ErrInvalidPayment is returned by NewPayment when required fields are missing
// or the amount is unusable.
var ErrInvalidPayment = errors.New("invalid payment")

// Payment is a single payment moving through its lifecycle.
//
// The entity carries everything the later stages need: the amount to move, the
// caller-supplied idempotency key that dedupes creation, and the ProviderRef
// the external processor hands back on authorize (required to capture or void).
type Payment struct {
	// ID is the unique payment identifier.
	ID string
	// Amount is the value to move, reusing the domain Money type.
	Amount ledger.Money
	// Status is the current lifecycle state.
	Status Status
	// IdempotencyKey is caller-supplied and prevents duplicate payments.
	IdempotencyKey string
	// ProviderRef is the reference returned by the provider on authorize;
	// it is needed for the later capture/void calls. Empty until authorized.
	ProviderRef string
	// CreatedAt is set once, when the payment is created.
	CreatedAt time.Time
	// UpdatedAt is refreshed on every successful transition.
	UpdatedAt time.Time
}

// NewPayment constructs a pending Payment, validating its required fields.
//
// A fresh payment always starts in StatusPending — no money action has been
// taken. CreatedAt and UpdatedAt are stamped to the same instant.
func NewPayment(id string, amount ledger.Money, idempotencyKey string) (*Payment, error) {
	var errs []error
	if id == "" {
		errs = append(errs, errors.New("payment ID is required"))
	}
	if idempotencyKey == "" {
		errs = append(errs, errors.New("idempotency key is required"))
	}
	if amount.Currency == "" {
		errs = append(errs, errors.New("payment amount currency is required"))
	}
	if amount.IsZero() {
		errs = append(errs, errors.New("payment amount must be non-zero"))
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("%w: %w", ErrInvalidPayment, errors.Join(errs...))
	}

	now := time.Now().UTC()
	return &Payment{
		ID:             id,
		Amount:         amount,
		Status:         StatusPending,
		IdempotencyKey: idempotencyKey,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// Authorize moves a pending payment to authorized and records the provider
// reference needed for a later capture or void. Legal only from pending.
func (p *Payment) Authorize(providerRef string) error {
	if err := p.transition(actionAuthorize); err != nil {
		return err
	}
	p.ProviderRef = providerRef
	return nil
}

// Fail moves a pending payment to the terminal failed state, used when the
// provider declines or errors during authorization. Legal only from pending.
func (p *Payment) Fail() error {
	return p.transition(actionFail)
}

// Capture moves an authorized payment to the terminal captured state, taking
// the held funds. Legal only from authorized.
func (p *Payment) Capture() error {
	return p.transition(actionCapture)
}

// Void moves an authorized payment to the terminal voided state, releasing the
// hold without taking funds. Legal only from authorized.
func (p *Payment) Void() error {
	return p.transition(actionVoid)
}

// IsTerminal reports whether the payment is in a state with no legal outgoing
// transition (captured, voided, or failed).
func (p *Payment) IsTerminal() bool {
	return len(transitions[p.Status]) == 0
}

// transition consults the transition table for the given action. On a legal
// move it advances Status and refreshes UpdatedAt; on an illegal move it leaves
// the payment untouched and returns an ErrInvalidTransition.
func (p *Payment) transition(a action) error {
	next, ok := transitions[p.Status][a]
	if !ok {
		return fmt.Errorf("%w: cannot %s from %s", ErrInvalidTransition, a, p.Status)
	}
	p.Status = next
	p.UpdatedAt = time.Now().UTC()
	return nil
}
