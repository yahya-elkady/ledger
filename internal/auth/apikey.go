// Package auth holds the pure cryptographic core of authentication: API-key
// generation/hashing, JWT issuance/validation, and password hashing. It is
// I/O-free — no database, no Redis, no HTTP — so it is fast and deterministic
// to unit-test. Persistence lives in internal/store; HTTP wiring lives in
// internal/api/middleware.
//
// Security invariants enforced here:
//   - API keys: only an HMAC-SHA256 hash is ever persisted; the plaintext is
//     returned once and never stored or logged.
//   - All key comparisons use hmac.Equal (constant-time) to avoid timing leaks.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/mr-tron/base58"
)

// KeyType is the kind of API key: publishable (client-side, limited) or secret
// (server-side, full). It maps to the human-facing prefix pk_ / sk_.
type KeyType string

const (
	KeyTypePublishable KeyType = "publishable"
	KeyTypeSecret      KeyType = "secret"
)

// prefixFor returns the short token prefix for a key type (sk_ / pk_).
func (t KeyType) prefixFor() (string, error) {
	switch t {
	case KeyTypeSecret:
		return "sk", nil
	case KeyTypePublishable:
		return "pk", nil
	default:
		return "", fmt.Errorf("unknown api key type %q", t)
	}
}

// keyRandomBytes is the number of cryptographically-random bytes in a key body.
const keyRandomBytes = 32

// keyPrefixDisplayLen is how many leading plaintext chars are stored as the
// display prefix (the type/mode tag plus a few key chars). Enough to identify a
// key in a list without revealing it.
const keyPrefixDisplayLen = 12

// GeneratedAPIKey is the result of minting a new API key. Plaintext is shown to
// the caller exactly once; only Hash and Prefix are safe to persist.
type GeneratedAPIKey struct {
	Plaintext string // full key (type+mode tag + base58 body) — return once, never store
	Hash      string // HMAC-SHA256(secret, plaintext), hex — safe to store
	Prefix    string // leading chars for display — safe to store
}

// APIKeyHasher mints and verifies API keys using a server-side HMAC secret.
// The same secret hashes a given plaintext deterministically, so a stored hash
// can be matched by recomputing it — without ever storing the plaintext.
type APIKeyHasher struct {
	secret []byte
}

// NewAPIKeyHasher constructs a hasher from the configured HMAC secret.
func NewAPIKeyHasher(secret string) *APIKeyHasher {
	return &APIKeyHasher{secret: []byte(secret)}
}

// Generate mints a new key of the given type and mode (test/live). The format
// is "<sk|pk>_<mode>_<base58(32 random bytes)>". It returns the plaintext (to
// show the merchant once), its hash, and its display prefix.
func (h *APIKeyHasher) Generate(keyType KeyType, mode string) (GeneratedAPIKey, error) {
	shortPrefix, err := keyType.prefixFor()
	if err != nil {
		return GeneratedAPIKey{}, err
	}
	if mode != "test" && mode != "live" {
		return GeneratedAPIKey{}, fmt.Errorf("invalid mode %q", mode)
	}

	raw := make([]byte, keyRandomBytes)
	if _, err := rand.Read(raw); err != nil {
		return GeneratedAPIKey{}, fmt.Errorf("reading random bytes: %w", err)
	}

	plaintext := fmt.Sprintf("%s_%s_%s", shortPrefix, mode, base58.Encode(raw))
	prefix := plaintext
	if len(prefix) > keyPrefixDisplayLen {
		prefix = prefix[:keyPrefixDisplayLen]
	}

	return GeneratedAPIKey{
		Plaintext: plaintext,
		Hash:      h.Hash(plaintext),
		Prefix:    prefix,
	}, nil
}

// Hash returns the hex-encoded HMAC-SHA256 of the plaintext key under the
// server secret. This is the only representation that touches storage.
func (h *APIKeyHasher) Hash(plaintext string) string {
	mac := hmac.New(sha256.New, h.secret)
	mac.Write([]byte(plaintext))
	return hex.EncodeToString(mac.Sum(nil))
}

// Validate reports whether plaintext hashes to storedHash, using a constant-time
// comparison to avoid timing side channels.
func (h *APIKeyHasher) Validate(plaintext, storedHash string) bool {
	computed := h.Hash(plaintext)
	return hmac.Equal([]byte(computed), []byte(storedHash))
}

// LooksLikeAPIKey is a cheap structural check (correct "<pk|sk>_<test|live>_"
// shape) used to short-circuit obviously-malformed Authorization headers before
// any hashing or DB work.
func LooksLikeAPIKey(token string) bool {
	for _, p := range []string{"sk_live_", "sk_test_", "pk_live_", "pk_test_"} {
		if strings.HasPrefix(token, p) {
			return true
		}
	}
	return false
}
