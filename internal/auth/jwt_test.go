package auth_test

import (
	"errors"
	"testing"
	"time"

	"github.com/yahya-elkady/ledger/internal/auth"
)

func newManager(t *testing.T, accessTTL, refreshTTL time.Duration) *auth.JWTManager {
	t.Helper()
	m, err := auth.NewJWTManager(
		"access_secret_at_least_32_chars_long_aaaa",
		"refresh_secret_at_least_32_chars_long_bbb",
		accessTTL, refreshTTL,
	)
	if err != nil {
		t.Fatalf("NewJWTManager: %v", err)
	}
	return m
}

func TestNewJWTManagerRejectsEqualSecrets(t *testing.T) {
	_, err := auth.NewJWTManager("same_secret_value", "same_secret_value", time.Minute, time.Hour)
	if err == nil {
		t.Error("expected error when access and refresh secrets are equal")
	}
}

func TestIssueAndValidateAccessToken(t *testing.T) {
	m := newManager(t, 15*time.Minute, 24*time.Hour)

	tok, err := m.IssueAccessToken("merchant-123", "live", []string{"read", "write"})
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}

	claims, err := m.ValidateAccessToken(tok)
	if err != nil {
		t.Fatalf("ValidateAccessToken: %v", err)
	}
	if claims.MerchantID != "merchant-123" {
		t.Errorf("MerchantID = %q, want merchant-123", claims.MerchantID)
	}
	if claims.Mode != "live" {
		t.Errorf("Mode = %q, want live", claims.Mode)
	}
	if claims.Subject != "merchant-123" {
		t.Errorf("Subject = %q, want merchant-123", claims.Subject)
	}
	if claims.ID == "" {
		t.Error("jti (ID) should be set")
	}
	if len(claims.Scope) != 2 {
		t.Errorf("Scope = %v, want 2 entries", claims.Scope)
	}
}

func TestExpiredAccessToken(t *testing.T) {
	m := newManager(t, -1*time.Minute, time.Hour) // already expired

	tok, err := m.IssueAccessToken("m1", "test", []string{"read"})
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}
	_, err = m.ValidateAccessToken(tok)
	if !errors.Is(err, auth.ErrTokenExpired) {
		t.Errorf("got %v, want ErrTokenExpired", err)
	}
}

func TestTamperedOrWrongSecretToken(t *testing.T) {
	m := newManager(t, time.Minute, time.Hour)

	t.Run("garbage token", func(t *testing.T) {
		if _, err := m.ValidateAccessToken("not.a.jwt"); !errors.Is(err, auth.ErrTokenInvalid) {
			t.Errorf("got %v, want ErrTokenInvalid", err)
		}
	})

	t.Run("access token is not a valid refresh token", func(t *testing.T) {
		// Signed with the access secret, so it must fail refresh validation
		// (different secret).
		tok, _ := m.IssueAccessToken("m1", "live", []string{"read"})
		if _, err := m.ParseRefreshToken(tok); !errors.Is(err, auth.ErrTokenInvalid) {
			t.Errorf("got %v, want ErrTokenInvalid (refresh secret differs)", err)
		}
	})
}

func TestRefreshTokenRoundTrip(t *testing.T) {
	m := newManager(t, time.Minute, 720*time.Hour)

	rt, err := m.IssueRefreshToken("merchant-9")
	if err != nil {
		t.Fatalf("IssueRefreshToken: %v", err)
	}
	if rt.JTI == "" {
		t.Error("refresh token should carry a jti")
	}
	if !rt.ExpiresAt.After(time.Now()) {
		t.Error("refresh token should expire in the future")
	}

	claims, err := m.ParseRefreshToken(rt.Token)
	if err != nil {
		t.Fatalf("ParseRefreshToken: %v", err)
	}
	if claims.MerchantID != "merchant-9" {
		t.Errorf("MerchantID = %q, want merchant-9", claims.MerchantID)
	}
	if claims.ID != rt.JTI {
		t.Errorf("parsed jti %q != issued jti %q", claims.ID, rt.JTI)
	}
}
