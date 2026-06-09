package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yahya-elkady/ledger/internal/models"
	db "github.com/yahya-elkady/ledger/internal/store/db"
)

// Payout/bank-account persistence errors.
var (
	ErrBankAccountNotFound = errors.New("bank account not found")
	ErrPayoutNotFound      = errors.New("payout not found")
)

// NewBankAccount is the input to CreateBankAccount.
type NewBankAccount struct {
	MerchantID      string
	Processor       string
	ProcessorAcctID string
	Last4           string
	BankName        string
	Currency        string
	IsDefault       bool
}

// NewPayout is the input to CreatePayout.
type NewPayout struct {
	MerchantID        string
	BankAccountID     string
	Amount            int64
	Currency          string
	Status            string
	Processor         string
	ProcessorPayoutID string
	IdempotencyKey    string
	Mode              string
	ArrivalDate       time.Time
}

// PayoutStore persists bank accounts and payouts. It satisfies both the
// BankAccountStore and PayoutStore handler interfaces.
type PayoutStore struct {
	pool    *pgxpool.Pool
	queries *db.Queries
}

// NewPayoutStore constructs a PayoutStore from an open pool.
func NewPayoutStore(pool *pgxpool.Pool) *PayoutStore {
	return &PayoutStore{pool: pool, queries: db.New(pool)}
}

// --- bank accounts ---------------------------------------------------------

func (s *PayoutStore) CreateBankAccount(ctx context.Context, b NewBankAccount) (*models.BankAccount, error) {
	mid, err := textToUUID(b.MerchantID)
	if err != nil {
		return nil, fmt.Errorf("invalid merchant id: %w", err)
	}
	row, err := s.queries.CreateBankAccount(ctx, db.CreateBankAccountParams{
		MerchantID:      mid,
		Processor:       b.Processor,
		ProcessorAcctID: b.ProcessorAcctID,
		Last4:           optionalText(b.Last4),
		BankName:        optionalText(b.BankName),
		Currency:        optionalChar3(b.Currency),
		IsDefault:       b.IsDefault,
	})
	if err != nil {
		return nil, fmt.Errorf("creating bank account: %w", err)
	}
	return bankAccountRowToModel(row), nil
}

func (s *PayoutStore) ListBankAccounts(ctx context.Context, merchantID string) ([]*models.BankAccount, error) {
	mid, err := textToUUID(merchantID)
	if err != nil {
		return nil, fmt.Errorf("invalid merchant id: %w", err)
	}
	rows, err := s.queries.ListBankAccounts(ctx, mid)
	if err != nil {
		return nil, fmt.Errorf("listing bank accounts: %w", err)
	}
	out := make([]*models.BankAccount, len(rows))
	for i, row := range rows {
		out[i] = bankAccountRowToModel(row)
	}
	return out, nil
}

func (s *PayoutStore) GetBankAccount(ctx context.Context, id, merchantID string) (*models.BankAccount, error) {
	bid, err := textToUUID(id)
	if err != nil {
		return nil, ErrBankAccountNotFound
	}
	mid, err := textToUUID(merchantID)
	if err != nil {
		return nil, ErrBankAccountNotFound
	}
	row, err := s.queries.GetBankAccount(ctx, db.GetBankAccountParams{ID: bid, MerchantID: mid})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrBankAccountNotFound
		}
		return nil, fmt.Errorf("getting bank account: %w", err)
	}
	return bankAccountRowToModel(row), nil
}

func (s *PayoutStore) SoftDeleteBankAccount(ctx context.Context, id, merchantID string) error {
	bid, err := textToUUID(id)
	if err != nil {
		return ErrBankAccountNotFound
	}
	mid, err := textToUUID(merchantID)
	if err != nil {
		return ErrBankAccountNotFound
	}
	if _, err := s.queries.SoftDeleteBankAccount(ctx, db.SoftDeleteBankAccountParams{ID: bid, MerchantID: mid}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrBankAccountNotFound
		}
		return fmt.Errorf("deleting bank account: %w", err)
	}
	return nil
}

// --- payouts ---------------------------------------------------------------

func (s *PayoutStore) CreatePayout(ctx context.Context, p NewPayout) (*models.Payout, error) {
	mid, err := textToUUID(p.MerchantID)
	if err != nil {
		return nil, fmt.Errorf("invalid merchant id: %w", err)
	}
	bid, err := textToUUID(p.BankAccountID)
	if err != nil {
		return nil, fmt.Errorf("invalid bank account id: %w", err)
	}
	row, err := s.queries.CreatePayout(ctx, db.CreatePayoutParams{
		MerchantID:        mid,
		BankAccountID:     bid,
		Amount:            p.Amount,
		Currency:          p.Currency,
		Status:            p.Status,
		Processor:         p.Processor,
		ProcessorPayoutID: optionalText(p.ProcessorPayoutID),
		IdempotencyKey:    optionalText(p.IdempotencyKey),
		Mode:              p.Mode,
		ArrivalDate:       dateFromTime(p.ArrivalDate),
	})
	if err != nil {
		return nil, fmt.Errorf("creating payout: %w", err)
	}
	return payoutRowToModel(row), nil
}

func (s *PayoutStore) GetPayout(ctx context.Context, id, merchantID, mode string) (*models.Payout, error) {
	pid, err := textToUUID(id)
	if err != nil {
		return nil, ErrPayoutNotFound
	}
	mid, err := textToUUID(merchantID)
	if err != nil {
		return nil, ErrPayoutNotFound
	}
	row, err := s.queries.GetPayout(ctx, db.GetPayoutParams{ID: pid, MerchantID: mid, Mode: mode})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrPayoutNotFound
		}
		return nil, fmt.Errorf("getting payout: %w", err)
	}
	return payoutRowToModel(row), nil
}

func (s *PayoutStore) ListPayouts(ctx context.Context, merchantID, mode string, limit int, cursor string) ([]*models.Payout, string, error) {
	mid, err := textToUUID(merchantID)
	if err != nil {
		return nil, "", fmt.Errorf("invalid merchant id: %w", err)
	}
	limit = clampLimit(limit)
	params := db.ListPayoutsParams{MerchantID: mid, Mode: mode, Limit: int32(limit + 1)}
	if cursor != "" {
		cur, err := decodeCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		params.CursorCreated = cur.createdAt
		params.CursorID = cur.id
	}
	rows, err := s.queries.ListPayouts(ctx, params)
	if err != nil {
		return nil, "", fmt.Errorf("listing payouts: %w", err)
	}
	next := ""
	if len(rows) > limit {
		last := rows[limit-1]
		rows = rows[:limit]
		next = encodeCursor(cursorKey{createdAt: last.CreatedAt, id: last.ID})
	}
	out := make([]*models.Payout, len(rows))
	for i, row := range rows {
		out[i] = payoutRowToModel(row)
	}
	return out, next, nil
}

// UpdateStatusByProcessorID updates a payout's status given its processor id —
// used when handling inbound processor webhooks.
func (s *PayoutStore) UpdateStatusByProcessorID(ctx context.Context, processorPayoutID, status, failureMessage string) (*models.Payout, error) {
	row, err := s.queries.UpdatePayoutStatusByProcessorID(ctx, db.UpdatePayoutStatusByProcessorIDParams{
		ProcessorPayoutID: optionalText(processorPayoutID),
		Status:            status,
		FailureMessage:    optionalText(failureMessage),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrPayoutNotFound
		}
		return nil, fmt.Errorf("updating payout by processor id: %w", err)
	}
	return payoutRowToModel(row), nil
}

// CountPendingPayouts returns the merchant's pending/in-transit payout count.
func (s *PayoutStore) CountPendingPayouts(ctx context.Context, merchantID, mode string) (int64, error) {
	mid, err := textToUUID(merchantID)
	if err != nil {
		return 0, fmt.Errorf("invalid merchant id: %w", err)
	}
	return s.queries.CountPendingPayouts(ctx, db.CountPendingPayoutsParams{MerchantID: mid, Mode: mode})
}

// --- mapping ---------------------------------------------------------------

func bankAccountRowToModel(row db.BankAccount) *models.BankAccount {
	return &models.BankAccount{
		ID:              uuidToText(row.ID),
		MerchantID:      uuidToText(row.MerchantID),
		Processor:       row.Processor,
		ProcessorAcctID: row.ProcessorAcctID,
		Last4:           derefText(row.Last4),
		BankName:        derefText(row.BankName),
		Currency:        derefText(row.Currency),
		IsDefault:       row.IsDefault,
		CreatedAt:       tsToTime(row.CreatedAt),
	}
}

func payoutRowToModel(row db.Payout) *models.Payout {
	return &models.Payout{
		ID:                uuidToText(row.ID),
		MerchantID:        uuidToText(row.MerchantID),
		BankAccountID:     uuidToText(row.BankAccountID),
		Amount:            row.Amount,
		Currency:          row.Currency,
		Status:            row.Status,
		Processor:         row.Processor,
		ProcessorPayoutID: derefText(row.ProcessorPayoutID),
		Mode:              row.Mode,
		FailureMessage:    derefText(row.FailureMessage),
		ArrivalDate:       dateToTimePtr(row.ArrivalDate),
		CreatedAt:         tsToTime(row.CreatedAt),
		UpdatedAt:         tsToTime(row.UpdatedAt),
	}
}

// optionalChar3 maps "" to NULL for a CHAR(3) currency column.
func optionalChar3(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// dateFromTime converts a time.Time to a pgtype.Date (NULL when zero).
func dateFromTime(t time.Time) pgtype.Date {
	if t.IsZero() {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: t, Valid: true}
}

// dateToTimePtr converts a nullable date to *time.Time.
func dateToTimePtr(d pgtype.Date) *time.Time {
	if !d.Valid {
		return nil
	}
	t := d.Time
	return &t
}
