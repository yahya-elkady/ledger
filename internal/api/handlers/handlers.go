// Package handlers implements the HTTP request handlers for the API. Handlers
// depend on small store interfaces (not concrete types) so they can be unit
// tested with fakes; the concrete sqlc-backed stores satisfy these interfaces in
// production wiring.
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/yahya-elkady/ledger/internal/api/middleware"
	"github.com/yahya-elkady/ledger/internal/api/respond"
	"github.com/yahya-elkady/ledger/internal/auth"
	"github.com/yahya-elkady/ledger/internal/models"
	"github.com/yahya-elkady/ledger/internal/processor"
	"github.com/yahya-elkady/ledger/internal/store"
	"github.com/yahya-elkady/ledger/internal/webhook"
)

// listResponse is the envelope for paginated/collection endpoints.
type listResponse[T any] struct {
	Data       []T    `json:"data"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// MerchantStore is the merchant persistence the handlers need.
type MerchantStore interface {
	CreateMerchant(ctx context.Context, email, passwordHash, businessName, mode string) (*models.Merchant, error)
	GetMerchantByEmail(ctx context.Context, email string) (*models.Merchant, string, error)
	GetMerchantByID(ctx context.Context, id string) (*models.Merchant, error)
}

// APIKeyStore is the API-key persistence the handlers need.
type APIKeyStore interface {
	SaveAPIKey(ctx context.Context, rec store.APIKeyRecord) (*store.APIKeyRecord, error)
	ListAPIKeysByMerchant(ctx context.Context, merchantID string) ([]*store.APIKeyRecord, error)
	GetAPIKeyByID(ctx context.Context, id, merchantID string) (*store.APIKeyRecord, error)
	RevokeAPIKey(ctx context.Context, keyID, merchantID string) (*store.APIKeyRecord, error)
}

// RefreshTokenStore is the refresh-token persistence the handlers need.
type RefreshTokenStore interface {
	SaveRefreshToken(ctx context.Context, rec store.RefreshTokenRecord) error
	RotateRefreshToken(ctx context.Context, oldHash string, next store.RefreshTokenRecord) error
	RevokeRefreshTokenByJTI(ctx context.Context, jti string) error
}

// CustomerStore is the customer persistence the handlers need.
type CustomerStore interface {
	CreateCustomer(ctx context.Context, merchantID, email, name string, metadata []byte) (*models.Customer, error)
	GetCustomer(ctx context.Context, id, merchantID string) (*models.Customer, error)
	ListCustomers(ctx context.Context, merchantID string, limit int, cursor string) ([]*models.Customer, string, error)
	UpdateCustomer(ctx context.Context, id, merchantID, email, name string, metadata []byte) (*models.Customer, error)
	SoftDeleteCustomer(ctx context.Context, id, merchantID string) error
}

// ChargeStore is the charge persistence the handlers need.
type ChargeStore interface {
	CreateCharge(ctx context.Context, c store.NewCharge) (*models.Charge, error)
	GetCharge(ctx context.Context, id, merchantID, mode string) (*models.Charge, error)
	ListCharges(ctx context.Context, merchantID, mode string, f store.ChargeFilter) ([]*models.Charge, string, error)
	SetRefund(ctx context.Context, id, merchantID string, refundedAmount int64, status string) (*models.Charge, error)
	UpdateStatusByProcessorID(ctx context.Context, processorChargeID, status, failureCode, failureMessage string) (*models.Charge, error)
}

// PlanStore is the plan persistence the handlers need.
type PlanStore interface {
	CreatePlan(ctx context.Context, p store.NewPlan) (*models.Plan, error)
	ListPlans(ctx context.Context, merchantID, mode string) ([]*models.Plan, error)
	GetPlan(ctx context.Context, id, merchantID, mode string) (*models.Plan, error)
	SoftDeletePlan(ctx context.Context, id, merchantID string) error
}

// SubscriptionStore is the subscription persistence the handlers need.
type SubscriptionStore interface {
	CreateSubscription(ctx context.Context, s store.NewSubscription) (*models.Subscription, error)
	GetSubscription(ctx context.Context, id, merchantID, mode string) (*models.Subscription, error)
	ListSubscriptions(ctx context.Context, merchantID, mode string, f store.SubscriptionFilter) ([]*models.Subscription, string, error)
	SetSubscriptionStatus(ctx context.Context, id, merchantID, status string, canceledAt bool) (*models.Subscription, error)
	UpdateStatusByProcessorID(ctx context.Context, processorSubID, status string) (*models.Subscription, error)
}

// BankAccountStore is the bank-account persistence the handlers need.
type BankAccountStore interface {
	CreateBankAccount(ctx context.Context, b store.NewBankAccount) (*models.BankAccount, error)
	ListBankAccounts(ctx context.Context, merchantID string) ([]*models.BankAccount, error)
	SoftDeleteBankAccount(ctx context.Context, id, merchantID string) error
}

// PayoutStore is the payout persistence the handlers need.
type PayoutStore interface {
	CreatePayout(ctx context.Context, p store.NewPayout) (*models.Payout, error)
	GetPayout(ctx context.Context, id, merchantID, mode string) (*models.Payout, error)
	ListPayouts(ctx context.Context, merchantID, mode string, limit int, cursor string) ([]*models.Payout, string, error)
	UpdateStatusByProcessorID(ctx context.Context, processorPayoutID, status, failureMessage string) (*models.Payout, error)
}

// DashboardStore provides the aggregates the dashboard renders.
type DashboardStore interface {
	ChargeStats(ctx context.Context, merchantID, mode string) (store.ChargeStats, error)
	CountActiveSubscriptions(ctx context.Context, merchantID, mode string) (int64, error)
	CountPendingPayouts(ctx context.Context, merchantID, mode string) (int64, error)
	RecentFailedCharges(ctx context.Context, merchantID, mode string, limit int) ([]*models.Charge, error)
}

// AuditLogger records mutations to the append-only audit log.
type AuditLogger interface {
	WriteAuditLog(ctx context.Context, e store.AuditEntry) error
}

// Handlers bundles the dependencies shared by all HTTP handlers. Store
// dependencies are interfaces (see Deps) so handlers unit-test with fakes.
type Handlers struct {
	merchants     MerchantStore
	apiKeys       APIKeyStore
	tokens        RefreshTokenStore
	customers     CustomerStore
	charges       ChargeStore
	plans         PlanStore
	subscriptions SubscriptionStore
	bankAccounts  BankAccountStore
	payouts       PayoutStore
	dashboard     DashboardStore
	audit         AuditLogger
	processor     processor.Processor
	stripeWebhook webhook.Verifier
	plaidWebhook  webhook.Verifier
	jwt           *auth.JWTManager
	hasher        *auth.APIKeyHasher
	accessTTL     time.Duration
}

// Deps is the full set of handler dependencies, passed to New. Fields may be nil
// in tests that only exercise a subset of handlers.
type Deps struct {
	Merchants     MerchantStore
	APIKeys       APIKeyStore
	Tokens        RefreshTokenStore
	Customers     CustomerStore
	Charges       ChargeStore
	Plans         PlanStore
	Subscriptions SubscriptionStore
	BankAccounts  BankAccountStore
	Payouts       PayoutStore
	Dashboard     DashboardStore
	Audit         AuditLogger
	Processor     processor.Processor
	StripeWebhook webhook.Verifier
	PlaidWebhook  webhook.Verifier
	JWT           *auth.JWTManager
	Hasher        *auth.APIKeyHasher
	AccessTTL     time.Duration
}

// New constructs the Handlers from its dependencies.
func New(d Deps) *Handlers {
	return &Handlers{
		merchants:     d.Merchants,
		apiKeys:       d.APIKeys,
		tokens:        d.Tokens,
		customers:     d.Customers,
		charges:       d.Charges,
		plans:         d.Plans,
		subscriptions: d.Subscriptions,
		bankAccounts:  d.BankAccounts,
		payouts:       d.Payouts,
		dashboard:     d.Dashboard,
		audit:         d.Audit,
		processor:     d.Processor,
		stripeWebhook: d.StripeWebhook,
		plaidWebhook:  d.PlaidWebhook,
		jwt:           d.JWT,
		hasher:        d.Hasher,
		accessTTL:     d.AccessTTL,
	}
}

// bind decodes a JSON request body into dst, rejecting unknown fields. On
// failure it writes a 400 validation_error and returns false.
func bind(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)) // cap body at 1 MiB
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		respond.Error(w, r, http.StatusBadRequest, respond.CodeValidationError, "invalid request body: "+err.Error())
		return false
	}
	return true
}

// auditActor derives the audit actor (type + id) from the authenticated context:
// API keys log the key id, JWT/dashboard logs the merchant id.
func auditActor(ctx context.Context) (actorType, actorID string) {
	switch middleware.Principal(ctx) {
	case middleware.PrincipalAPIKey:
		return "api_key", middleware.APIKeyID(ctx)
	case middleware.PrincipalJWT:
		return "jwt", middleware.MerchantID(ctx)
	default:
		return "system", ""
	}
}

// clientIP returns the request's client IP without a port (best effort).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// respondNotFoundOr500 maps a "not found" sentinel to 404 and anything else to
// 500, the common error tail for single-resource lookups.
func respondNotFoundOr500(w http.ResponseWriter, r *http.Request, err, notFound error, msg string) {
	if errors.Is(err, notFound) {
		respond.Error(w, r, http.StatusNotFound, respond.CodeNotFound, msg)
		return
	}
	respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
}

// auditMutation records a mutation against the authenticated principal. It never
// blocks the primary operation (errors are swallowed by writeAudit).
func (h *Handlers) auditMutation(r *http.Request, action, resource, resourceID string) {
	actorType, actorID := auditActor(r.Context())
	h.writeAudit(r, store.AuditEntry{
		MerchantID: middleware.MerchantID(r.Context()),
		ActorType:  actorType,
		ActorID:    actorID,
		Action:     action,
		Resource:   resource,
		ResourceID: resourceID,
		IP:         clientIP(r),
	})
}
