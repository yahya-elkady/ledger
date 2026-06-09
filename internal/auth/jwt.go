package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Auth token errors. Callers (middleware) distinguish expiry from other
// failures to return the right HTTP error code.
var (
	// ErrTokenExpired is returned when a token's exp claim is in the past.
	ErrTokenExpired = errors.New("token expired")
	// ErrTokenInvalid is returned for any other validation failure (bad
	// signature, malformed token, wrong signing method).
	ErrTokenInvalid = errors.New("token invalid")
)

// AccessClaims are the claims carried by a short-lived access token. The custom
// fields drive authorization (mode isolation + scope) without a DB round-trip.
type AccessClaims struct {
	MerchantID string   `json:"merchant_id"`
	Mode       string   `json:"mode"`  // test | live
	Scope      []string `json:"scope"` // read | write | admin
	jwt.RegisteredClaims
}

// RefreshClaims are the claims of a long-lived refresh token. It carries only
// identity and a unique ID (jti) so it can be revoked by id; authorization
// detail lives on the access token.
type RefreshClaims struct {
	MerchantID string `json:"merchant_id"`
	jwt.RegisteredClaims
}

// RefreshToken is a freshly-issued refresh token. Token is the signed string
// shown to the client; JTI matches refresh_tokens.jti for revocation.
type RefreshToken struct {
	Token     string
	JTI       string
	ExpiresAt time.Time
}

// JWTManager issues and validates access and refresh tokens. Access and refresh
// tokens are signed with SEPARATE secrets (build.md security rule) so a leaked
// access secret cannot forge refresh tokens, and vice versa.
type JWTManager struct {
	accessSecret  []byte
	refreshSecret []byte
	accessTTL     time.Duration
	refreshTTL    time.Duration
}

// NewJWTManager constructs a manager. It rejects equal secrets — access and
// refresh must differ.
func NewJWTManager(accessSecret, refreshSecret string, accessTTL, refreshTTL time.Duration) (*JWTManager, error) {
	if accessSecret == "" || refreshSecret == "" {
		return nil, errors.New("jwt secrets must not be empty")
	}
	if accessSecret == refreshSecret {
		return nil, errors.New("jwt access and refresh secrets must differ")
	}
	return &JWTManager{
		accessSecret:  []byte(accessSecret),
		refreshSecret: []byte(refreshSecret),
		accessTTL:     accessTTL,
		refreshTTL:    refreshTTL,
	}, nil
}

// IssueAccessToken signs an HS256 access token carrying merchant identity, mode,
// and scope, expiring after the configured access TTL.
func (m *JWTManager) IssueAccessToken(merchantID, mode string, scope []string) (string, error) {
	now := time.Now()
	jti, err := newJTI()
	if err != nil {
		return "", err
	}
	claims := AccessClaims{
		MerchantID: merchantID,
		Mode:       mode,
		Scope:      scope,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   merchantID,
			ID:        jti,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(m.accessTTL)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.accessSecret)
}

// IssueRefreshToken signs an HS256 refresh token (separate secret, longer TTL).
// The caller hashes Token before persisting it; the plaintext is never stored.
func (m *JWTManager) IssueRefreshToken(merchantID string) (RefreshToken, error) {
	now := time.Now()
	expiresAt := now.Add(m.refreshTTL)
	jti, err := newJTI()
	if err != nil {
		return RefreshToken{}, err
	}
	claims := RefreshClaims{
		MerchantID: merchantID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   merchantID,
			ID:        jti,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.refreshSecret)
	if err != nil {
		return RefreshToken{}, err
	}
	return RefreshToken{Token: signed, JTI: jti, ExpiresAt: expiresAt}, nil
}

// ValidateAccessToken verifies an access token's signature and expiry and
// returns its claims. It maps expiry to ErrTokenExpired and all other failures
// to ErrTokenInvalid.
func (m *JWTManager) ValidateAccessToken(tokenString string) (*AccessClaims, error) {
	claims := &AccessClaims{}
	if err := parseInto(tokenString, claims, m.accessSecret); err != nil {
		return nil, err
	}
	return claims, nil
}

// ParseRefreshToken verifies a refresh token's signature and expiry and returns
// its claims (notably the jti used to look it up / revoke it).
func (m *JWTManager) ParseRefreshToken(tokenString string) (*RefreshClaims, error) {
	claims := &RefreshClaims{}
	if err := parseInto(tokenString, claims, m.refreshSecret); err != nil {
		return nil, err
	}
	return claims, nil
}

// parseInto verifies tokenString against secret, requires HMAC signing, and
// fills claims. Expiry is normalized to ErrTokenExpired.
func parseInto(tokenString string, claims jwt.Claims, secret []byte) error {
	_, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return secret, nil
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return ErrTokenExpired
		}
		return fmt.Errorf("%w: %v", ErrTokenInvalid, err)
	}
	return nil
}

// newJTI returns a random 128-bit token id, hex-encoded.
func newJTI() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating jti: %w", err)
	}
	return hex.EncodeToString(b), nil
}
