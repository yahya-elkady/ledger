package handlers

import (
	"net/http"

	"github.com/yahya-elkady/ledger/internal/api/middleware"
	"github.com/yahya-elkady/ledger/internal/api/respond"
	"github.com/yahya-elkady/ledger/internal/models"
)

// recentFailedLimit caps how many recent failed charges the overview returns.
const recentFailedLimit = 10

// overviewResponse aggregates the merchant's current activity for one mode.
type overviewResponse struct {
	Mode                string           `json:"mode"`
	ChargeCount         int64            `json:"charge_count"`
	ChargeSucceeded     int64            `json:"charge_succeeded"`
	ChargeFailed        int64            `json:"charge_failed"`
	GrossVolume         int64            `json:"gross_volume"` // succeeded, minor units
	ActiveSubscriptions int64            `json:"active_subscriptions"`
	PendingPayouts      int64            `json:"pending_payouts"`
	RecentFailedCharges []*models.Charge `json:"recent_failed_charges"`
}

// DashboardOverview returns headline aggregates for the authenticated merchant,
// scoped to the request's mode (test/live).
func (h *Handlers) DashboardOverview(w http.ResponseWriter, r *http.Request) {
	merchantID := middleware.MerchantID(r.Context())
	mode := middleware.Mode(r.Context())

	stats, err := h.dashboard.ChargeStats(r.Context(), merchantID, mode)
	if err != nil {
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}
	activeSubs, err := h.dashboard.CountActiveSubscriptions(r.Context(), merchantID, mode)
	if err != nil {
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}
	pendingPayouts, err := h.dashboard.CountPendingPayouts(r.Context(), merchantID, mode)
	if err != nil {
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}
	failed, err := h.dashboard.RecentFailedCharges(r.Context(), merchantID, mode, recentFailedLimit)
	if err != nil {
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}

	respond.JSON(w, r, http.StatusOK, overviewResponse{
		Mode:                mode,
		ChargeCount:         stats.TotalCount,
		ChargeSucceeded:     stats.SucceededCount,
		ChargeFailed:        stats.FailedCount,
		GrossVolume:         stats.SucceededVolume,
		ActiveSubscriptions: activeSubs,
		PendingPayouts:      pendingPayouts,
		RecentFailedCharges: failed,
	})
}

// DashboardTransactions returns a unified, paginated transaction list. For P2
// this surfaces charges (the primary transaction type); payouts can be folded
// in later. Mode-isolated and merchant-scoped.
func (h *Handlers) DashboardTransactions(w http.ResponseWriter, r *http.Request) {
	merchantID := middleware.MerchantID(r.Context())
	mode := middleware.Mode(r.Context())

	charges, next, err := h.charges.ListCharges(r.Context(), merchantID, mode, parseChargeFilter(r))
	if err != nil {
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}
	respond.JSON(w, r, http.StatusOK, listResponse[*models.Charge]{Data: charges, NextCursor: next})
}
