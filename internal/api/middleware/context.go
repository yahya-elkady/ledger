// Package middleware holds the HTTP middleware stack: authentication
// (API key + JWT), scope enforcement, and mode isolation. Authenticated request
// attributes (merchant id, mode, scope, principal type) are carried through the
// request context using the typed keys and accessors below.
package middleware

import "context"

// contextKey is an unexported type so these keys cannot collide with keys from
// other packages stored in the same context.
type contextKey int

const (
	merchantIDKey contextKey = iota
	modeKey
	scopeKey
	principalKey
	apiKeyIDKey
)

// PrincipalType identifies how a request was authenticated.
type PrincipalType string

const (
	PrincipalAPIKey PrincipalType = "api_key"
	PrincipalJWT    PrincipalType = "jwt"
)

// withAuth returns a child context carrying the authenticated attributes.
func withAuth(ctx context.Context, merchantID, mode string, scope []string, principal PrincipalType) context.Context {
	ctx = context.WithValue(ctx, merchantIDKey, merchantID)
	ctx = context.WithValue(ctx, modeKey, mode)
	ctx = context.WithValue(ctx, scopeKey, scope)
	ctx = context.WithValue(ctx, principalKey, principal)
	return ctx
}

// MerchantID returns the authenticated merchant id, or "" if unauthenticated.
func MerchantID(ctx context.Context) string {
	id, _ := ctx.Value(merchantIDKey).(string)
	return id
}

// Mode returns the request's test/live mode, or "" if unset.
func Mode(ctx context.Context) string {
	mode, _ := ctx.Value(modeKey).(string)
	return mode
}

// Scope returns the authenticated scopes, or nil if unset.
func Scope(ctx context.Context) []string {
	scope, _ := ctx.Value(scopeKey).([]string)
	return scope
}

// Principal returns how the request was authenticated, or "" if unauthenticated.
func Principal(ctx context.Context) PrincipalType {
	p, _ := ctx.Value(principalKey).(PrincipalType)
	return p
}

// withAPIKeyID attaches the authenticating API key's id, used as the rate-limit
// subject for machine clients.
func withAPIKeyID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, apiKeyIDKey, id)
}

// APIKeyID returns the authenticating API key's id, or "" for JWT/unauthenticated.
func APIKeyID(ctx context.Context) string {
	id, _ := ctx.Value(apiKeyIDKey).(string)
	return id
}
