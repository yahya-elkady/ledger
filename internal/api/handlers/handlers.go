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
	"github.com/yahya-elkady/ledger/internal/store"
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

// AuditLogger records mutations to the append-only audit log.
type AuditLogger interface {
	WriteAuditLog(ctx context.Context, e store.AuditEntry) error
}

// Handlers bundles the dependencies shared by all HTTP handlers.
type Handlers struct {
	merchants MerchantStore
	apiKeys   APIKeyStore
	tokens    RefreshTokenStore
	customers CustomerStore
	audit     AuditLogger
	jwt       *auth.JWTManager
	hasher    *auth.APIKeyHasher
	accessTTL time.Duration
}

// Config carries the non-store dependencies and tunables for the handlers.
type Config struct {
	JWT       *auth.JWTManager
	Hasher    *auth.APIKeyHasher
	AccessTTL time.Duration
}

// New constructs the Handlers from its dependencies.
func New(merchants MerchantStore, apiKeys APIKeyStore, tokens RefreshTokenStore, customers CustomerStore, audit AuditLogger, cfg Config) *Handlers {
	return &Handlers{
		merchants: merchants,
		apiKeys:   apiKeys,
		tokens:    tokens,
		customers: customers,
		audit:     audit,
		jwt:       cfg.JWT,
		hasher:    cfg.Hasher,
		accessTTL: cfg.AccessTTL,
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
