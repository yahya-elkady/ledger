package auth_test

import (
	"strings"
	"testing"

	"github.com/yahya-elkady/ledger/internal/auth"
)

const testSecret = "test_hmac_secret_at_least_32_chars_long_xx"

func TestGenerateAPIKey(t *testing.T) {
	h := auth.NewAPIKeyHasher(testSecret)

	t.Run("format and prefix", func(t *testing.T) {
		got, err := h.Generate(auth.KeyTypeSecret, "live")
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if !strings.HasPrefix(got.Plaintext, "sk_live_") {
			t.Errorf("plaintext = %q, want sk_live_ prefix", got.Plaintext)
		}
		if !strings.HasPrefix(got.Plaintext, got.Prefix) {
			t.Errorf("prefix %q is not a prefix of plaintext %q", got.Prefix, got.Plaintext)
		}
		if got.Hash == "" || got.Hash == got.Plaintext {
			t.Errorf("hash must be set and differ from plaintext")
		}
		if !h.Validate(got.Plaintext, got.Hash) {
			t.Error("generated key should validate against its own hash")
		}
	})

	t.Run("publishable + test mode", func(t *testing.T) {
		got, err := h.Generate(auth.KeyTypePublishable, "test")
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if !strings.HasPrefix(got.Plaintext, "pk_test_") {
			t.Errorf("plaintext = %q, want pk_test_ prefix", got.Plaintext)
		}
	})

	t.Run("keys are unique", func(t *testing.T) {
		a, _ := h.Generate(auth.KeyTypeSecret, "live")
		b, _ := h.Generate(auth.KeyTypeSecret, "live")
		if a.Plaintext == b.Plaintext {
			t.Error("two generated keys collided")
		}
	})

	t.Run("invalid type and mode rejected", func(t *testing.T) {
		if _, err := h.Generate(auth.KeyType("bogus"), "live"); err == nil {
			t.Error("expected error for bad key type")
		}
		if _, err := h.Generate(auth.KeyTypeSecret, "prod"); err == nil {
			t.Error("expected error for bad mode")
		}
	})
}

func TestHashAndValidate(t *testing.T) {
	h := auth.NewAPIKeyHasher(testSecret)

	t.Run("hash is deterministic", func(t *testing.T) {
		first := h.Hash("sk_live_abc")
		second := h.Hash("sk_live_abc")
		if first != second {
			t.Error("hash should be deterministic for the same input")
		}
	})

	t.Run("validate rejects wrong key", func(t *testing.T) {
		hash := h.Hash("sk_live_correct")
		if h.Validate("sk_live_wrong", hash) {
			t.Error("validate should reject a non-matching key")
		}
	})

	t.Run("different secret yields different hash", func(t *testing.T) {
		other := auth.NewAPIKeyHasher("another_secret_at_least_32_chars_long_yyy")
		if h.Hash("sk_live_abc") == other.Hash("sk_live_abc") {
			t.Error("different secrets must produce different hashes")
		}
	})
}

func TestLooksLikeAPIKey(t *testing.T) {
	cases := map[string]bool{
		"sk_live_xxx":       true,
		"pk_test_xxx":       true,
		"sk_test_xxx":       true,
		"pk_live_xxx":       true,
		"eyJhbG.jwt.token":  false,
		"":                  false,
		"bearer sk_live_xx": false,
	}
	for in, want := range cases {
		if got := auth.LooksLikeAPIKey(in); got != want {
			t.Errorf("LooksLikeAPIKey(%q) = %v, want %v", in, got, want)
		}
	}
}
