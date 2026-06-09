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

// Billing persistence errors.
var (
	ErrPlanNotFound         = errors.New("plan not found")
	ErrSubscriptionNotFound = errors.New("subscription not found")
)

// NewPlan is the input to CreatePlan.
type NewPlan struct {
	MerchantID      string
	Name            string
	Amount          int64
	Currency        string
	Interval        string
	IntervalCount   int
	ProcessorPlanID string
	Mode            string
}

// NewSubscription is the input to CreateSubscription.
type NewSubscription struct {
	MerchantID         string
	CustomerID         string
	PlanID             string
	PaymentMethodID    string
	Status             string
	ProcessorSubID     string
	CurrentPeriodStart time.Time
	CurrentPeriodEnd   time.Time
	TrialEnd           time.Time
	Mode               string
	Metadata           []byte
}

// SubscriptionFilter narrows ListSubscriptions.
type SubscriptionFilter struct {
	Status string
	Limit  int
	Cursor string
}

// BillingStore persists plans and subscriptions. It satisfies both the
// PlanStore and SubscriptionStore handler interfaces.
type BillingStore struct {
	pool    *pgxpool.Pool
	queries *db.Queries
}

// NewBillingStore constructs a BillingStore from an open pool.
func NewBillingStore(pool *pgxpool.Pool) *BillingStore {
	return &BillingStore{pool: pool, queries: db.New(pool)}
}

// --- plans -----------------------------------------------------------------

func (s *BillingStore) CreatePlan(ctx context.Context, p NewPlan) (*models.Plan, error) {
	mid, err := textToUUID(p.MerchantID)
	if err != nil {
		return nil, fmt.Errorf("invalid merchant id: %w", err)
	}
	row, err := s.queries.CreatePlan(ctx, db.CreatePlanParams{
		MerchantID:      mid,
		Name:            p.Name,
		Amount:          p.Amount,
		Currency:        p.Currency,
		Interval:        p.Interval,
		IntervalCount:   int16(p.IntervalCount),
		ProcessorPlanID: optionalText(p.ProcessorPlanID),
		Mode:            p.Mode,
	})
	if err != nil {
		return nil, fmt.Errorf("creating plan: %w", err)
	}
	return planRowToModel(row), nil
}

func (s *BillingStore) ListPlans(ctx context.Context, merchantID, mode string) ([]*models.Plan, error) {
	mid, err := textToUUID(merchantID)
	if err != nil {
		return nil, fmt.Errorf("invalid merchant id: %w", err)
	}
	rows, err := s.queries.ListPlans(ctx, db.ListPlansParams{MerchantID: mid, Mode: mode})
	if err != nil {
		return nil, fmt.Errorf("listing plans: %w", err)
	}
	out := make([]*models.Plan, len(rows))
	for i, row := range rows {
		out[i] = planRowToModel(row)
	}
	return out, nil
}

func (s *BillingStore) GetPlan(ctx context.Context, id, merchantID, mode string) (*models.Plan, error) {
	pid, err := textToUUID(id)
	if err != nil {
		return nil, ErrPlanNotFound
	}
	mid, err := textToUUID(merchantID)
	if err != nil {
		return nil, ErrPlanNotFound
	}
	row, err := s.queries.GetPlan(ctx, db.GetPlanParams{ID: pid, MerchantID: mid, Mode: mode})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrPlanNotFound
		}
		return nil, fmt.Errorf("getting plan: %w", err)
	}
	return planRowToModel(row), nil
}

func (s *BillingStore) SoftDeletePlan(ctx context.Context, id, merchantID string) error {
	pid, err := textToUUID(id)
	if err != nil {
		return ErrPlanNotFound
	}
	mid, err := textToUUID(merchantID)
	if err != nil {
		return ErrPlanNotFound
	}
	if _, err := s.queries.SoftDeletePlan(ctx, db.SoftDeletePlanParams{ID: pid, MerchantID: mid}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPlanNotFound
		}
		return fmt.Errorf("deleting plan: %w", err)
	}
	return nil
}

// --- subscriptions ---------------------------------------------------------

func (s *BillingStore) CreateSubscription(ctx context.Context, sub NewSubscription) (*models.Subscription, error) {
	mid, err := textToUUID(sub.MerchantID)
	if err != nil {
		return nil, fmt.Errorf("invalid merchant id: %w", err)
	}
	cid, err := textToUUID(sub.CustomerID)
	if err != nil {
		return nil, fmt.Errorf("invalid customer id: %w", err)
	}
	pid, err := textToUUID(sub.PlanID)
	if err != nil {
		return nil, fmt.Errorf("invalid plan id: %w", err)
	}
	pmid, err := textToUUID(sub.PaymentMethodID)
	if err != nil {
		return nil, fmt.Errorf("invalid payment method id: %w", err)
	}
	row, err := s.queries.CreateSubscription(ctx, db.CreateSubscriptionParams{
		MerchantID:         mid,
		CustomerID:         cid,
		PlanID:             pid,
		PaymentMethodID:    pmid,
		Status:             sub.Status,
		ProcessorSubID:     optionalText(sub.ProcessorSubID),
		CurrentPeriodStart: timeToTS(sub.CurrentPeriodStart),
		CurrentPeriodEnd:   timeToTS(sub.CurrentPeriodEnd),
		TrialEnd:           timeToTS(sub.TrialEnd),
		Mode:               sub.Mode,
		Metadata:           sub.Metadata,
	})
	if err != nil {
		return nil, fmt.Errorf("creating subscription: %w", err)
	}
	return subscriptionRowToModel(row), nil
}

func (s *BillingStore) GetSubscription(ctx context.Context, id, merchantID, mode string) (*models.Subscription, error) {
	sid, err := textToUUID(id)
	if err != nil {
		return nil, ErrSubscriptionNotFound
	}
	mid, err := textToUUID(merchantID)
	if err != nil {
		return nil, ErrSubscriptionNotFound
	}
	row, err := s.queries.GetSubscription(ctx, db.GetSubscriptionParams{ID: sid, MerchantID: mid, Mode: mode})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSubscriptionNotFound
		}
		return nil, fmt.Errorf("getting subscription: %w", err)
	}
	return subscriptionRowToModel(row), nil
}

func (s *BillingStore) ListSubscriptions(ctx context.Context, merchantID, mode string, f SubscriptionFilter) ([]*models.Subscription, string, error) {
	mid, err := textToUUID(merchantID)
	if err != nil {
		return nil, "", fmt.Errorf("invalid merchant id: %w", err)
	}
	limit := clampLimit(f.Limit)
	params := db.ListSubscriptionsParams{MerchantID: mid, Mode: mode, Limit: int32(limit + 1)}
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
	rows, err := s.queries.ListSubscriptions(ctx, params)
	if err != nil {
		return nil, "", fmt.Errorf("listing subscriptions: %w", err)
	}
	next := ""
	if len(rows) > limit {
		last := rows[limit-1]
		rows = rows[:limit]
		next = encodeCursor(cursorKey{createdAt: last.CreatedAt, id: last.ID})
	}
	out := make([]*models.Subscription, len(rows))
	for i, row := range rows {
		out[i] = subscriptionRowToModel(row)
	}
	return out, next, nil
}

// SetSubscriptionStatus updates a subscription's status, optionally stamping
// canceled_at (when setCanceled is true).
func (s *BillingStore) SetSubscriptionStatus(ctx context.Context, id, merchantID, status string, setCanceled bool) (*models.Subscription, error) {
	sid, err := textToUUID(id)
	if err != nil {
		return nil, ErrSubscriptionNotFound
	}
	mid, err := textToUUID(merchantID)
	if err != nil {
		return nil, ErrSubscriptionNotFound
	}
	row, err := s.queries.SetSubscriptionStatus(ctx, db.SetSubscriptionStatusParams{
		ID: sid, MerchantID: mid, Status: status, SetCanceled: setCanceled,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSubscriptionNotFound
		}
		return nil, fmt.Errorf("updating subscription status: %w", err)
	}
	return subscriptionRowToModel(row), nil
}

// UpdateStatusByProcessorID updates a subscription's status given its processor
// id — used when handling inbound processor webhooks.
func (s *BillingStore) UpdateStatusByProcessorID(ctx context.Context, processorSubID, status string) (*models.Subscription, error) {
	row, err := s.queries.UpdateSubscriptionStatusByProcessorID(ctx, db.UpdateSubscriptionStatusByProcessorIDParams{
		ProcessorSubID: optionalText(processorSubID),
		Status:         status,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSubscriptionNotFound
		}
		return nil, fmt.Errorf("updating subscription by processor id: %w", err)
	}
	return subscriptionRowToModel(row), nil
}

// CountActiveSubscriptions returns the merchant's active subscription count.
func (s *BillingStore) CountActiveSubscriptions(ctx context.Context, merchantID, mode string) (int64, error) {
	mid, err := textToUUID(merchantID)
	if err != nil {
		return 0, fmt.Errorf("invalid merchant id: %w", err)
	}
	return s.queries.CountActiveSubscriptions(ctx, db.CountActiveSubscriptionsParams{MerchantID: mid, Mode: mode})
}

// --- mapping ---------------------------------------------------------------

func planRowToModel(row db.Plan) *models.Plan {
	return &models.Plan{
		ID:              uuidToText(row.ID),
		MerchantID:      uuidToText(row.MerchantID),
		Name:            row.Name,
		Amount:          row.Amount,
		Currency:        row.Currency,
		Interval:        row.Interval,
		IntervalCount:   int(row.IntervalCount),
		ProcessorPlanID: derefText(row.ProcessorPlanID),
		Mode:            row.Mode,
		CreatedAt:       tsToTime(row.CreatedAt),
	}
}

func subscriptionRowToModel(row db.Subscription) *models.Subscription {
	return &models.Subscription{
		ID:                 uuidToText(row.ID),
		MerchantID:         uuidToText(row.MerchantID),
		CustomerID:         uuidToText(row.CustomerID),
		PlanID:             uuidToText(row.PlanID),
		PaymentMethodID:    uuidToText(row.PaymentMethodID),
		Status:             row.Status,
		ProcessorSubID:     derefText(row.ProcessorSubID),
		CurrentPeriodStart: tsToTimePtr(row.CurrentPeriodStart),
		CurrentPeriodEnd:   tsToTimePtr(row.CurrentPeriodEnd),
		TrialEnd:           tsToTimePtr(row.TrialEnd),
		CanceledAt:         tsToTimePtr(row.CanceledAt),
		Mode:               row.Mode,
		Metadata:           row.Metadata,
		CreatedAt:          tsToTime(row.CreatedAt),
		UpdatedAt:          tsToTime(row.UpdatedAt),
	}
}

// tsToTimePtr converts a nullable timestamptz to a *time.Time (nil when NULL).
func tsToTimePtr(ts pgtype.Timestamptz) *time.Time {
	if !ts.Valid {
		return nil
	}
	t := ts.Time
	return &t
}
