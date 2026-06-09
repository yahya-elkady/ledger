package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yahya-elkady/ledger/internal/models"
	db "github.com/yahya-elkady/ledger/internal/store/db"
)

// Charge persistence errors.
var (
	// ErrChargeNotFound is returned when no charge matches a lookup.
	ErrChargeNotFound = errors.New("charge not found")
	// ErrIdempotencyConflict is returned when a charge with the same
	// idempotency key already exists (the charges.idempotency_key UNIQUE
	// constraint) — the database backstop behind the idempotency middleware.
	ErrIdempotencyConflict = errors.New("idempotency key already used")
)

// NewCharge is the input to CreateCharge.
type NewCharge struct {
	MerchantID        string
	CustomerID        string // optional
	PaymentMethodID   string // optional
	Amount            int64
	Currency          string
	Status            string
	Processor         string
	ProcessorChargeID string
	IdempotencyKey    string
	Mode              string
	FailureCode       string
	FailureMessage    string
	Metadata          []byte
}

// ChargeFilter narrows a ListCharges query.
type ChargeFilter struct {
	Status string // optional exact status filter
	Limit  int
	Cursor string
}

// ChargeStore persists charges, scoped to merchant and mode.
type ChargeStore struct {
	pool    *pgxpool.Pool
	queries *db.Queries
}

// NewChargeStore constructs a ChargeStore from an open pool.
func NewChargeStore(pool *pgxpool.Pool) *ChargeStore {
	return &ChargeStore{pool: pool, queries: db.New(pool)}
}

// CreateCharge inserts a charge. A duplicate idempotency key is reported as
// ErrIdempotencyConflict.
func (s *ChargeStore) CreateCharge(ctx context.Context, c NewCharge) (*models.Charge, error) {
	mid, err := textToUUID(c.MerchantID)
	if err != nil {
		return nil, fmt.Errorf("invalid merchant id: %w", err)
	}
	row, err := s.queries.CreateCharge(ctx, db.CreateChargeParams{
		MerchantID:        mid,
		CustomerID:        optionalUUID(c.CustomerID),
		PaymentMethodID:   optionalUUID(c.PaymentMethodID),
		Amount:            c.Amount,
		Currency:          c.Currency,
		Status:            c.Status,
		Processor:         c.Processor,
		ProcessorChargeID: optionalText(c.ProcessorChargeID),
		IdempotencyKey:    optionalText(c.IdempotencyKey),
		Mode:              c.Mode,
		FailureCode:       optionalText(c.FailureCode),
		FailureMessage:    optionalText(c.FailureMessage),
		Metadata:          c.Metadata,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return nil, ErrIdempotencyConflict
		}
		return nil, fmt.Errorf("creating charge: %w", err)
	}
	return chargeRowToModel(row), nil
}

// GetCharge loads a charge scoped to merchant + mode.
func (s *ChargeStore) GetCharge(ctx context.Context, id, merchantID, mode string) (*models.Charge, error) {
	cid, err := textToUUID(id)
	if err != nil {
		return nil, ErrChargeNotFound
	}
	mid, err := textToUUID(merchantID)
	if err != nil {
		return nil, ErrChargeNotFound
	}
	row, err := s.queries.GetCharge(ctx, db.GetChargeParams{ID: cid, MerchantID: mid, Mode: mode})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrChargeNotFound
		}
		return nil, fmt.Errorf("getting charge: %w", err)
	}
	return chargeRowToModel(row), nil
}

// ListCharges returns a page of charges (newest first) plus a next cursor.
func (s *ChargeStore) ListCharges(ctx context.Context, merchantID, mode string, f ChargeFilter) ([]*models.Charge, string, error) {
	mid, err := textToUUID(merchantID)
	if err != nil {
		return nil, "", fmt.Errorf("invalid merchant id: %w", err)
	}
	limit := clampLimit(f.Limit)

	params := db.ListChargesParams{MerchantID: mid, Mode: mode, Limit: int32(limit + 1)}
	if f.Status != "" {
		params.Status = &f.Status
	}
	if f.Cursor != "" {
		cur, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		params.CursorCreated = cur.createdAt
		params.CursorID = cur.id
	}

	rows, err := s.queries.ListCharges(ctx, params)
	if err != nil {
		return nil, "", fmt.Errorf("listing charges: %w", err)
	}

	next := ""
	if len(rows) > limit {
		last := rows[limit-1]
		rows = rows[:limit]
		next = encodeCursor(cursorKey{createdAt: last.CreatedAt, id: last.ID})
	}
	out := make([]*models.Charge, len(rows))
	for i, row := range rows {
		out[i] = chargeRowToModel(row)
	}
	return out, next, nil
}

// SetRefund updates a charge's refunded amount and status, scoped to merchant.
func (s *ChargeStore) SetRefund(ctx context.Context, id, merchantID string, refundedAmount int64, status string) (*models.Charge, error) {
	cid, err := textToUUID(id)
	if err != nil {
		return nil, ErrChargeNotFound
	}
	mid, err := textToUUID(merchantID)
	if err != nil {
		return nil, ErrChargeNotFound
	}
	row, err := s.queries.UpdateChargeRefund(ctx, db.UpdateChargeRefundParams{
		ID: cid, MerchantID: mid, RefundedAmount: refundedAmount, Status: status,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrChargeNotFound
		}
		return nil, fmt.Errorf("updating charge refund: %w", err)
	}
	return chargeRowToModel(row), nil
}

// UpdateStatusByProcessorID updates a charge's status given its processor id —
// used when handling inbound processor webhooks. Returns ErrChargeNotFound if no
// charge carries that processor id.
func (s *ChargeStore) UpdateStatusByProcessorID(ctx context.Context, processorChargeID, status, failureCode, failureMessage string) (*models.Charge, error) {
	row, err := s.queries.UpdateChargeStatusByProcessorID(ctx, db.UpdateChargeStatusByProcessorIDParams{
		ProcessorChargeID: optionalText(processorChargeID),
		Status:            status,
		FailureCode:       optionalText(failureCode),
		FailureMessage:    optionalText(failureMessage),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrChargeNotFound
		}
		return nil, fmt.Errorf("updating charge by processor id: %w", err)
	}
	return chargeRowToModel(row), nil
}

func chargeRowToModel(row db.Charge) *models.Charge {
	return &models.Charge{
		ID:                uuidToText(row.ID),
		MerchantID:        uuidToText(row.MerchantID),
		CustomerID:        uuidPtrToText(row.CustomerID),
		PaymentMethodID:   uuidPtrToText(row.PaymentMethodID),
		Amount:            row.Amount,
		Currency:          row.Currency,
		Status:            row.Status,
		Processor:         row.Processor,
		ProcessorChargeID: derefText(row.ProcessorChargeID),
		Mode:              row.Mode,
		FailureCode:       derefText(row.FailureCode),
		FailureMessage:    derefText(row.FailureMessage),
		RefundedAmount:    row.RefundedAmount,
		Metadata:          row.Metadata,
		CreatedAt:         tsToTime(row.CreatedAt),
		UpdatedAt:         tsToTime(row.UpdatedAt),
	}
}

// uuidPtrToText renders a nullable pgtype.UUID column as text ("" when NULL).
func uuidPtrToText(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return uuidToText(u)
}
