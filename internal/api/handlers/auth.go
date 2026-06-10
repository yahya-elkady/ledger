package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/yahya-elkady/ledger/internal/api/respond"
	"github.com/yahya-elkady/ledger/internal/auth"
	"github.com/yahya-elkady/ledger/internal/models"
	"github.com/yahya-elkady/ledger/internal/store"
)

// minPasswordLen is the minimum acceptable dashboard password length.
const minPasswordLen = 8

// registerRequest is the POST /v1/auth/register body.
type registerRequest struct {
	Email        string `json:"email"`
	Password     string `json:"password"`
	BusinessName string `json:"business_name"`
}

// loginRequest is the POST /v1/auth/login body.
type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// refreshRequest / logoutRequest carry a refresh token in the body.
type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// tokenResponse is the token bundle returned by register/login/refresh.
type tokenResponse struct {
	AccessToken  string           `json:"access_token"`
	RefreshToken string           `json:"refresh_token"`
	TokenType    string           `json:"token_type"`
	ExpiresIn    int              `json:"expires_in"` // access token lifetime, seconds
	Merchant     *models.Merchant `json:"merchant,omitempty"`
}

// dashboardScope is the scope embedded in a merchant's dashboard JWT: full
// control of their own account.
var dashboardScope = []string{"admin"}

// Register creates a merchant, then issues an access + refresh token pair.
//
// PCI-DSS: passwords are bcrypt-hashed (cost >= 12) before storage; the
// plaintext password is never persisted or logged.
func (h *Handlers) Register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if !bind(w, r, &req) {
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if msg, param, ok := validateRegister(req); !ok {
		respond.ErrorParam(w, r, http.StatusBadRequest, respond.CodeValidationError, msg, param)
		return
	}

	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}

	// New merchants start in test mode (build.md merchants.mode default).
	const mode = "test"
	merchant, err := h.merchants.CreateMerchant(r.Context(), req.Email, passwordHash, req.BusinessName, mode)
	if err != nil {
		if errors.Is(err, store.ErrEmailTaken) {
			respond.ErrorParam(w, r, http.StatusConflict, respond.CodeValidationError, "email already registered", "email")
			return
		}
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}

	tokens, err := h.issueTokens(r, merchant)
	if err != nil {
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}
	tokens.Merchant = merchant

	h.writeAudit(r, store.AuditEntry{
		MerchantID: merchant.ID,
		ActorType:  "system",
		ActorID:    merchant.ID,
		Action:     "merchant.created",
		Resource:   "merchants",
		ResourceID: merchant.ID,
		IP:         clientIP(r),
	})

	respond.JSON(w, r, http.StatusCreated, tokens)
}

// Login verifies credentials and issues a fresh token pair. It returns the same
// generic error whether the email is unknown or the password is wrong, so the
// response cannot be used to enumerate registered emails.
func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !bind(w, r, &req) {
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))

	merchant, passwordHash, err := h.merchants.GetMerchantByEmail(r.Context(), req.Email)
	if err != nil || !auth.CheckPassword(req.Password, passwordHash) {
		// Abuse signal. The email is NOT logged (PII + enumeration aid); the IP
		// is enough to correlate brute-force attempts with the rate limiter.
		log.Ctx(r.Context()).Warn().Str("ip", clientIP(r)).Msg("auth failure: login rejected")
		respond.Error(w, r, http.StatusUnauthorized, respond.CodeAuthenticationRequired, "invalid email or password")
		return
	}
	log.Ctx(r.Context()).Info().Str("merchant_id", merchant.ID).Msg("merchant login")

	tokens, err := h.issueTokens(r, merchant)
	if err != nil {
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}
	respond.JSON(w, r, http.StatusOK, tokens)
}

// Refresh validates a refresh token and rotates it: the presented token is
// revoked and a brand-new access + refresh pair is issued (atomically). Reusing
// an already-rotated token is rejected.
func (h *Handlers) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if !bind(w, r, &req) {
		return
	}
	if req.RefreshToken == "" {
		respond.ErrorParam(w, r, http.StatusBadRequest, respond.CodeValidationError, "refresh_token is required", "refresh_token")
		return
	}

	claims, err := h.jwt.ParseRefreshToken(req.RefreshToken)
	if err != nil {
		respond.Error(w, r, http.StatusUnauthorized, respond.CodeAuthenticationRequired, "invalid refresh token")
		return
	}

	merchant, err := h.merchants.GetMerchantByID(r.Context(), claims.MerchantID)
	if err != nil {
		respond.Error(w, r, http.StatusUnauthorized, respond.CodeAuthenticationRequired, "invalid refresh token")
		return
	}

	// Issue the new pair, then atomically swap it in for the old one.
	access, err := h.jwt.IssueAccessToken(merchant.ID, merchant.Mode, dashboardScope)
	if err != nil {
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}
	newRefresh, err := h.jwt.IssueRefreshToken(merchant.ID)
	if err != nil {
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}

	oldHash := h.hasher.Hash(req.RefreshToken)
	next := store.RefreshTokenRecord{
		MerchantID: merchant.ID,
		TokenHash:  h.hasher.Hash(newRefresh.Token),
		JTI:        newRefresh.JTI,
		ExpiresAt:  newRefresh.ExpiresAt,
	}
	if err := h.tokens.RotateRefreshToken(r.Context(), oldHash, next); err != nil {
		// Token absent or already revoked → possible refresh-token replay/theft;
		// this is a security event, not routine noise.
		log.Ctx(r.Context()).Warn().Str("merchant_id", merchant.ID).Str("ip", clientIP(r)).
			Msg("auth failure: refresh token reuse or unknown token rejected")
		respond.Error(w, r, http.StatusUnauthorized, respond.CodeAuthenticationRequired, "invalid refresh token")
		return
	}
	log.Ctx(r.Context()).Info().Str("merchant_id", merchant.ID).Str("jti", newRefresh.JTI).
		Msg("refresh token rotated")

	respond.JSON(w, r, http.StatusOK, tokenResponse{
		AccessToken:  access,
		RefreshToken: newRefresh.Token,
		TokenType:    "Bearer",
		ExpiresIn:    int(h.accessTTL.Seconds()),
	})
}

// Logout revokes the presented refresh token. It is idempotent — logging out an
// already-revoked or unknown token still returns 204.
func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if !bind(w, r, &req) {
		return
	}
	if claims, err := h.jwt.ParseRefreshToken(req.RefreshToken); err == nil {
		if err := h.tokens.RevokeRefreshTokenByJTI(r.Context(), claims.ID); err == nil {
			log.Ctx(r.Context()).Info().Str("merchant_id", claims.MerchantID).
				Str("jti", claims.ID).Msg("merchant logout: refresh token revoked")
		}
		// Unknown/already-revoked token: logout stays idempotent and silent.
	}
	w.WriteHeader(http.StatusNoContent)
}

// issueTokens mints an access token and a refresh token for a merchant and
// persists the refresh token's hash.
func (h *Handlers) issueTokens(r *http.Request, merchant *models.Merchant) (tokenResponse, error) {
	access, err := h.jwt.IssueAccessToken(merchant.ID, merchant.Mode, dashboardScope)
	if err != nil {
		return tokenResponse{}, err
	}
	refresh, err := h.jwt.IssueRefreshToken(merchant.ID)
	if err != nil {
		return tokenResponse{}, err
	}
	if err := h.tokens.SaveRefreshToken(r.Context(), store.RefreshTokenRecord{
		MerchantID: merchant.ID,
		TokenHash:  h.hasher.Hash(refresh.Token),
		JTI:        refresh.JTI,
		ExpiresAt:  refresh.ExpiresAt,
	}); err != nil {
		return tokenResponse{}, err
	}
	return tokenResponse{
		AccessToken:  access,
		RefreshToken: refresh.Token,
		TokenType:    "Bearer",
		ExpiresIn:    int(h.accessTTL.Seconds()),
	}, nil
}

// writeAudit records an audit entry and mirrors it as a structured INFO log, so
// every mutation leaves both a DB trail and a log trail. An audit-write failure
// is logged at ERROR (it must be investigated) but never blocks the primary
// operation that already committed.
func (h *Handlers) writeAudit(r *http.Request, e store.AuditEntry) {
	log.Ctx(r.Context()).Info().Str("action", e.Action).Str("resource", e.Resource).
		Str("resource_id", e.ResourceID).Str("merchant_id", e.MerchantID).
		Str("actor_type", e.ActorType).Msg("mutation")
	if err := h.audit.WriteAuditLog(r.Context(), e); err != nil {
		log.Ctx(r.Context()).Error().Err(err).Str("action", e.Action).
			Str("resource_id", e.ResourceID).Msg("audit log write failed")
	}
}

// validateRegister checks registration input, returning (message, param, ok).
func validateRegister(req registerRequest) (string, string, bool) {
	if !looksLikeEmail(req.Email) {
		return "a valid email is required", "email", false
	}
	if len(req.Password) < minPasswordLen {
		return "password must be at least 8 characters", "password", false
	}
	if strings.TrimSpace(req.BusinessName) == "" {
		return "business_name is required", "business_name", false
	}
	return "", "", true
}

// looksLikeEmail is a deliberately permissive structural email check (the
// authoritative check is a verification email, out of scope here).
func looksLikeEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 {
		return false
	}
	return strings.IndexByte(s[at+1:], '.') >= 0
}
