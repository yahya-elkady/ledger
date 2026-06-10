package handlers

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/yahya-elkady/ledger/internal/api/middleware"
	"github.com/yahya-elkady/ledger/internal/api/respond"
	"github.com/yahya-elkady/ledger/internal/auth"
	"github.com/yahya-elkady/ledger/internal/store"
)

// createAPIKeyRequest is the POST /v1/apikeys body.
type createAPIKeyRequest struct {
	Name  string   `json:"name"`
	Type  string   `json:"type"`  // publishable | secret
	Mode  string   `json:"mode"`  // test | live
	Scope []string `json:"scope"` // read | write | admin
}

// apiKeyResponse is the safe view of a key: never the hash or plaintext.
type apiKeyResponse struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Prefix    string     `json:"key_prefix"`
	Type      string     `json:"type"`
	Mode      string     `json:"mode"`
	Scope     []string   `json:"scope"`
	LastUsed  *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// createAPIKeyResponse includes the plaintext key, returned exactly once.
type createAPIKeyResponse struct {
	apiKeyResponse
	Key string `json:"key"` // plaintext — shown once, never retrievable again
}

var validScopes = map[string]bool{"read": true, "write": true, "admin": true}

// CreateAPIKey mints a new API key for the authenticated merchant. The plaintext
// is returned once in the response; only its HMAC hash is stored.
func (h *Handlers) CreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var req createAPIKeyRequest
	if !bind(w, r, &req) {
		return
	}
	if msg, param, ok := validateCreateAPIKey(req); !ok {
		respond.ErrorParam(w, r, http.StatusBadRequest, respond.CodeValidationError, msg, param)
		return
	}

	merchantID := middleware.MerchantID(r.Context())
	keyType := auth.KeyType(req.Type)

	gen, err := h.hasher.Generate(keyType, req.Mode)
	if err != nil {
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}

	rec, err := h.apiKeys.SaveAPIKey(r.Context(), store.APIKeyRecord{
		MerchantID: merchantID,
		Name:       req.Name,
		KeyHash:    gen.Hash,
		KeyPrefix:  gen.Prefix,
		Type:       req.Type,
		Mode:       req.Mode,
		Scope:      req.Scope,
	})
	if err != nil {
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}

	actorType, actorID := auditActor(r.Context())
	h.writeAudit(r, store.AuditEntry{
		MerchantID: merchantID,
		ActorType:  actorType,
		ActorID:    actorID,
		Action:     "apikey.created",
		Resource:   "api_keys",
		ResourceID: rec.ID,
		IP:         clientIP(r),
	})

	respond.JSON(w, r, http.StatusCreated, createAPIKeyResponse{
		apiKeyResponse: toAPIKeyResponse(rec),
		Key:            gen.Plaintext,
	})
}

// ListAPIKeys returns the merchant's active keys (metadata only).
func (h *Handlers) ListAPIKeys(w http.ResponseWriter, r *http.Request) {
	merchantID := middleware.MerchantID(r.Context())
	recs, err := h.apiKeys.ListAPIKeysByMerchant(r.Context(), merchantID)
	if err != nil {
		respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
		return
	}
	out := make([]apiKeyResponse, len(recs))
	for i, rec := range recs {
		out[i] = toAPIKeyResponse(rec)
	}
	respond.JSON(w, r, http.StatusOK, listResponse[apiKeyResponse]{Data: out})
}

// GetAPIKey returns one key's metadata, scoped to the merchant.
func (h *Handlers) GetAPIKey(w http.ResponseWriter, r *http.Request) {
	merchantID := middleware.MerchantID(r.Context())
	rec, err := h.apiKeys.GetAPIKeyByID(r.Context(), chi.URLParam(r, "id"), merchantID)
	if err != nil {
		respondNotFoundOr500(w, r, err, store.ErrAPIKeyNotFound, "api key not found")
		return
	}
	respond.JSON(w, r, http.StatusOK, toAPIKeyResponse(rec))
}

// DeleteAPIKey revokes a key: soft delete in the store, then immediate Redis
// cache eviction so the revoked key cannot authenticate for the rest of the
// auth-cache TTL.
func (h *Handlers) DeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	merchantID := middleware.MerchantID(r.Context())
	rec, err := h.apiKeys.RevokeAPIKey(r.Context(), chi.URLParam(r, "id"), merchantID)
	if err != nil {
		respondNotFoundOr500(w, r, err, store.ErrAPIKeyNotFound, "api key not found")
		return
	}
	if h.keyCache != nil {
		h.keyCache.InvalidateAPIKeyCache(r.Context(), rec.KeyHash)
	}

	actorType, actorID := auditActor(r.Context())
	h.writeAudit(r, store.AuditEntry{
		MerchantID: merchantID,
		ActorType:  actorType,
		ActorID:    actorID,
		Action:     "apikey.revoked",
		Resource:   "api_keys",
		ResourceID: rec.ID,
		IP:         clientIP(r),
	})

	w.WriteHeader(http.StatusNoContent)
}

// validateCreateAPIKey validates the create request.
func validateCreateAPIKey(req createAPIKeyRequest) (string, string, bool) {
	if req.Name == "" {
		return "name is required", "name", false
	}
	if req.Type != string(auth.KeyTypePublishable) && req.Type != string(auth.KeyTypeSecret) {
		return "type must be 'publishable' or 'secret'", "type", false
	}
	if req.Mode != "test" && req.Mode != "live" {
		return "mode must be 'test' or 'live'", "mode", false
	}
	if len(req.Scope) == 0 {
		return "at least one scope is required", "scope", false
	}
	for _, s := range req.Scope {
		if !validScopes[s] {
			return "scope must be one of read, write, admin", "scope", false
		}
	}
	return "", "", true
}

// toAPIKeyResponse maps a stored record to its safe response view.
func toAPIKeyResponse(rec *store.APIKeyRecord) apiKeyResponse {
	resp := apiKeyResponse{
		ID:        rec.ID,
		Name:      rec.Name,
		Prefix:    rec.KeyPrefix,
		Type:      rec.Type,
		Mode:      rec.Mode,
		Scope:     rec.Scope,
		CreatedAt: rec.CreatedAt,
	}
	if !rec.LastUsedAt.IsZero() {
		resp.LastUsed = &rec.LastUsedAt
	}
	if !rec.ExpiresAt.IsZero() {
		resp.ExpiresAt = &rec.ExpiresAt
	}
	return resp
}
