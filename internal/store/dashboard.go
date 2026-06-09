package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yahya-elkady/ledger/internal/models"
	db "github.com/yahya-elkady/ledger/internal/store/db"
)

// ChargeStats summarizes a merchant's charge activity for one mode.
type ChargeStats struct {
	TotalCount      int64
	SucceededCount  int64
	SucceededVolume int64 // minor units
	FailedCount     int64
}

// DashboardStore answers the aggregate queries the merchant dashboard renders.
// It composes the charge, billing, and payout tables; all reads are scoped to
// merchant + mode so the dashboard never mixes test and live data.
type DashboardStore struct {
	pool    *pgxpool.Pool
	queries *db.Queries
}

// NewDashboardStore constructs a DashboardStore from an open pool.
func NewDashboardStore(pool *pgxpool.Pool) *DashboardStore {
	return &DashboardStore{pool: pool, queries: db.New(pool)}
}

// ChargeStats returns charge counts and succeeded volume for a merchant+mode.
func (s *DashboardStore) ChargeStats(ctx context.Context, merchantID, mode string) (ChargeStats, error) {
	mid, err := textToUUID(merchantID)
	if err != nil {
		return ChargeStats{}, fmt.Errorf("invalid merchant id: %w", err)
	}
	row, err := s.queries.ChargeStatsByMode(ctx, db.ChargeStatsByModeParams{MerchantID: mid, Mode: mode})
	if err != nil {
		return ChargeStats{}, fmt.Errorf("charge stats: %w", err)
	}
	return ChargeStats{
		TotalCount:      row.TotalCount,
		SucceededCount:  row.SucceededCount,
		SucceededVolume: row.SucceededVolume,
		FailedCount:     row.FailedCount,
	}, nil
}

// CountActiveSubscriptions returns the active subscription count for merchant+mode.
func (s *DashboardStore) CountActiveSubscriptions(ctx context.Context, merchantID, mode string) (int64, error) {
	mid, err := textToUUID(merchantID)
	if err != nil {
		return 0, fmt.Errorf("invalid merchant id: %w", err)
	}
	return s.queries.CountActiveSubscriptions(ctx, db.CountActiveSubscriptionsParams{MerchantID: mid, Mode: mode})
}

// CountPendingPayouts returns the pending/in-transit payout count for merchant+mode.
func (s *DashboardStore) CountPendingPayouts(ctx context.Context, merchantID, mode string) (int64, error) {
	mid, err := textToUUID(merchantID)
	if err != nil {
		return 0, fmt.Errorf("invalid merchant id: %w", err)
	}
	return s.queries.CountPendingPayouts(ctx, db.CountPendingPayoutsParams{MerchantID: mid, Mode: mode})
}

// RecentFailedCharges returns the most recent failed charges for merchant+mode.
func (s *DashboardStore) RecentFailedCharges(ctx context.Context, merchantID, mode string, limit int) ([]*models.Charge, error) {
	mid, err := textToUUID(merchantID)
	if err != nil {
		return nil, fmt.Errorf("invalid merchant id: %w", err)
	}
	rows, err := s.queries.RecentFailedCharges(ctx, db.RecentFailedChargesParams{
		MerchantID: mid, Mode: mode, Limit: int32(clampLimit(limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("recent failed charges: %w", err)
	}
	out := make([]*models.Charge, len(rows))
	for i, row := range rows {
		out[i] = chargeRowToModel(row)
	}
	return out, nil
}
