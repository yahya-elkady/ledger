package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"

	"github.com/yahya-elkady/ledger/internal/api/respond"
	"github.com/yahya-elkady/ledger/internal/auth"
	"github.com/yahya-elkady/ledger/internal/store"
)

// apiKeyCacheTTL is how long a validated API key lookup is cached in Redis to
// avoid a DB hit on every request. Revocation explicitly evicts the entry, so a
// revoked key stops working immediately rather than after the TTL.
const apiKeyCacheTTL = 5 * time.Minute

// APIKeyStore is the slice of persistence the API-key middleware needs.
// *store.AuthStore satisfies it; tests can supply a fake.
type APIKeyStore interface {
	GetAPIKeyByHash(ctx context.Context, hash string) (*store.APIKeyRecord, error)
}

// Authenticator builds the authentication middlewares. It composes the pure
// auth crypto (hasher, JWT manager) with persistence and a Redis cache.
type Authenticator struct {
	keys   APIKeyStore
	jwt    *auth.JWTManager
	hasher *auth.APIKeyHasher
	redis  *redis.Client // may be nil; caching is then skipped
}

// NewAuthenticator wires the authenticator to its dependencies.
func NewAuthenticator(keys APIKeyStore, jwtMgr *auth.JWTManager, hasher *auth.APIKeyHasher, rdb *redis.Client) *Authenticator {
	return &Authenticator{keys: keys, jwt: jwtMgr, hasher: hasher, redis: rdb}
}

// APIKeyMiddleware authenticates machine clients via `Authorization: Bearer
// sk_...`. It hashes the presented key, resolves it (Redis cache → DB), checks
// it is neither revoked nor expired, and injects merchant id / mode / scope into
// the request context. Any failure returns 401 without revealing why in detail.
func (a *Authenticator) APIKeyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok || !auth.LooksLikeAPIKey(token) {
			respond.Error(w, r, http.StatusUnauthorized, respond.CodeAuthenticationRequired,
				"a valid API key is required")
			return
		}

		hash := a.hasher.Hash(token)
		rec, err := a.resolveAPIKey(r.Context(), hash)
		if err != nil {
			if errors.Is(err, store.ErrAPIKeyNotFound) {
				respond.Error(w, r, http.StatusUnauthorized, respond.CodeInvalidAPIKey, "invalid API key")
				return
			}
			log.Ctx(r.Context()).Error().Err(err).Msg("api key lookup failed")
			respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
			return
		}

		if !rec.IsActive(time.Now()) {
			// Revoked or expired — same opaque response as an unknown key.
			respond.Error(w, r, http.StatusUnauthorized, respond.CodeInvalidAPIKey, "invalid API key")
			return
		}

		ctx := withAuth(r.Context(), rec.MerchantID, rec.Mode, rec.Scope, PrincipalAPIKey)
		ctx = withAPIKeyID(ctx, rec.ID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// JWTMiddleware authenticates dashboard users via `Authorization: Bearer <jwt>`.
// It validates signature + expiry and injects the token's claims into context.
func (a *Authenticator) JWTMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			respond.Error(w, r, http.StatusUnauthorized, respond.CodeAuthenticationRequired,
				"a bearer token is required")
			return
		}

		claims, err := a.jwt.ValidateAccessToken(token)
		if err != nil {
			if errors.Is(err, auth.ErrTokenExpired) {
				respond.Error(w, r, http.StatusUnauthorized, respond.CodeTokenExpired, "access token expired")
				return
			}
			respond.Error(w, r, http.StatusUnauthorized, respond.CodeAuthenticationRequired, "invalid token")
			return
		}

		ctx := withAuth(r.Context(), claims.MerchantID, claims.Mode, claims.Scope, PrincipalJWT)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireScope returns middleware that enforces a minimum scope. Scopes are
// hierarchical: admin implies write implies read. Must run after an auth
// middleware (which populates the scope in context).
func RequireScope(required string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !scopeSatisfies(Scope(r.Context()), required) {
				respond.Error(w, r, http.StatusForbidden, respond.CodeInsufficientScope,
					"this API key lacks the required scope")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ModeMiddleware enforces that the request carries a valid test/live mode (set
// by the auth middleware) before it reaches handlers, so downstream queries can
// safely filter by mode and never serve cross-mode data.
func ModeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mode := Mode(r.Context())
		if mode != "test" && mode != "live" {
			// Reaching here without a mode means auth middleware did not run —
			// a wiring bug, not a client error.
			log.Ctx(r.Context()).Error().Str("mode", mode).Msg("request reached ModeMiddleware without a valid mode")
			respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// InvalidateAPIKeyCache evicts a key's cached lookup. The revoke handler calls
// this (with the key's stored hash) so revocation takes effect immediately.
func (a *Authenticator) InvalidateAPIKeyCache(ctx context.Context, keyHash string) {
	if a.redis == nil {
		return
	}
	if err := a.redis.Del(ctx, apiKeyCacheKey(keyHash)).Err(); err != nil {
		log.Ctx(ctx).Warn().Err(err).Msg("failed to evict api key cache entry")
	}
}

// resolveAPIKey returns the key record from the Redis cache if present,
// otherwise from the database (caching the result).
func (a *Authenticator) resolveAPIKey(ctx context.Context, hash string) (*store.APIKeyRecord, error) {
	if rec := a.getCachedAPIKey(ctx, hash); rec != nil {
		return rec, nil
	}
	rec, err := a.keys.GetAPIKeyByHash(ctx, hash)
	if err != nil {
		return nil, err
	}
	a.cacheAPIKey(ctx, hash, rec)
	return rec, nil
}

func (a *Authenticator) getCachedAPIKey(ctx context.Context, hash string) *store.APIKeyRecord {
	if a.redis == nil {
		return nil
	}
	raw, err := a.redis.Get(ctx, apiKeyCacheKey(hash)).Bytes()
	if err != nil {
		return nil // cache miss or Redis down — fall back to DB
	}
	var rec store.APIKeyRecord
	if json.Unmarshal(raw, &rec) != nil {
		return nil
	}
	return &rec
}

func (a *Authenticator) cacheAPIKey(ctx context.Context, hash string, rec *store.APIKeyRecord) {
	if a.redis == nil {
		return
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return
	}
	if err := a.redis.Set(ctx, apiKeyCacheKey(hash), raw, apiKeyCacheTTL).Err(); err != nil {
		log.Ctx(ctx).Warn().Err(err).Msg("failed to cache api key lookup")
	}
}

func apiKeyCacheKey(hash string) string { return "apikey:" + hash }

// bearerToken extracts the token from an `Authorization: Bearer <token>` header.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(h[len(prefix):])
	return token, token != ""
}

// scopeRank orders the hierarchical scopes; a higher rank includes all below it.
var scopeRank = map[string]int{"read": 1, "write": 2, "admin": 3}

// scopeSatisfies reports whether any held scope meets or exceeds the required
// scope under the read < write < admin hierarchy.
func scopeSatisfies(have []string, required string) bool {
	need, ok := scopeRank[required]
	if !ok {
		return false // unknown required scope: deny
	}
	for _, s := range have {
		if scopeRank[s] >= need {
			return true
		}
	}
	return false
}
