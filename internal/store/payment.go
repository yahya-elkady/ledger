package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yahya-elkady/ledger/internal/ledger"
	"github.com/yahya-elkady/ledger/internal/payment"
	db "github.com/yahya-elkady/ledger/internal/store/db"
)

// PaymentStore persists payment.Payment entities to the database.
//
// It mirrors LedgerStore: it owns the translation between the domain Payment
// and the sqlc-generated DB params, keeping that mapping in one place. It
// satisfies the payment.PaymentRepository interface the service depends on.
type PaymentStore struct {
	pool    *pgxpool.Pool
	queries *db.Queries
}

// NewPaymentStore constructs a PaymentStore from an open connection pool.
func NewPaymentStore(pool *pgxpool.Pool) *PaymentStore {
	return &PaymentStore{
		pool:    pool,
		queries: db.New(pool),
	}
}

// CreatePayment inserts a new payment row. The UNIQUE constraint on
// idempotency_key rejects a duplicate key at the database level.
func (s *PaymentStore) CreatePayment(ctx context.Context, p *payment.Payment) error {
	_, err := s.queries.CreatePayment(ctx, db.CreatePaymentParams{
		ID:             p.ID,
		Amount:         p.Amount.Amount,
		Currency:       p.Amount.Currency,
		Status:         string(p.Status),
		IdempotencyKey: p.IdempotencyKey,
		ProviderRef:    p.ProviderRef,
		CreatedAt:      pgtype.Timestamptz{Time: p.CreatedAt, Valid: true},
		UpdatedAt:      pgtype.Timestamptz{Time: p.UpdatedAt, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("creating payment %s: %w", p.ID, err)
	}
	return nil
}

// GetPayment loads a payment by ID. Returns payment.ErrPaymentNotFound if no
// row matches, so callers can branch on a missing payment with errors.Is.
func (s *PaymentStore) GetPayment(ctx context.Context, id string) (*payment.Payment, error) {
	row, err := s.queries.GetPayment(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, payment.ErrPaymentNotFound
		}
		return nil, fmt.Errorf("getting payment %s: %w", id, err)
	}
	return rowToPayment(row), nil
}

// GetPaymentByIdempotencyKey loads a payment by its idempotency key. Returns
// payment.ErrPaymentNotFound when no payment carries that key yet.
func (s *PaymentStore) GetPaymentByIdempotencyKey(ctx context.Context, key string) (*payment.Payment, error) {
	row, err := s.queries.GetPaymentByIdempotencyKey(ctx, key)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, payment.ErrPaymentNotFound
		}
		return nil, fmt.Errorf("getting payment by idempotency key: %w", err)
	}
	return rowToPayment(row), nil
}

// UpdatePaymentStatus persists a transition: the new status, the provider
// reference (set on authorize), and the refreshed updated_at timestamp.
func (s *PaymentStore) UpdatePaymentStatus(ctx context.Context, p *payment.Payment) error {
	_, err := s.queries.UpdatePaymentStatus(ctx, db.UpdatePaymentStatusParams{
		ID:          p.ID,
		Status:      string(p.Status),
		ProviderRef: p.ProviderRef,
		UpdatedAt:   pgtype.Timestamptz{Time: p.UpdatedAt, Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return payment.ErrPaymentNotFound
		}
		return fmt.Errorf("updating payment %s: %w", p.ID, err)
	}
	return nil
}

// rowToPayment converts a DB row into a domain Payment value. This is the
// translation going DB → domain, the inverse of the params built above.
func rowToPayment(row db.Payment) *payment.Payment {
	return &payment.Payment{
		ID:             row.ID,
		Amount:         ledger.MustMoney(row.Amount, row.Currency),
		Status:         payment.Status(row.Status),
		IdempotencyKey: row.IdempotencyKey,
		ProviderRef:    row.ProviderRef,
		CreatedAt:      row.CreatedAt.Time,
		UpdatedAt:      row.UpdatedAt.Time,
	}
}
