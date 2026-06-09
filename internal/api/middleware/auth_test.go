package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yahya-elkady/ledger/internal/auth"
	"github.com/yahya-elkady/ledger/internal/store"
)

// fakeKeyStore is an in-memory APIKeyStore keyed by HMAC hash.
type fakeKeyStore struct {
	byHash map[string]*store.APIKeyRecord
}

func (f *fakeKeyStore) GetAPIKeyByHash(_ context.Context, hash string) (*store.APIKeyRecord, error) {
	if r, ok := f.byHash[hash]; ok {
		return r, nil
	}
	return nil, store.ErrAPIKeyNotFound
}

func testJWTManager(t *testing.T) *auth.JWTManager {
	t.Helper()
	m, err := auth.NewJWTManager(
		"access_secret_at_least_32_chars_long_aaaa",
		"refresh_secret_at_least_32_chars_long_bbb",
		15*time.Minute, 24*time.Hour,
	)
	if err != nil {
		t.Fatalf("NewJWTManager: %v", err)
	}
	return m
}

// okHandler records the context it saw and returns 200.
func okHandler(seen *context.Context) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = r.Context()
		w.WriteHeader(http.StatusOK)
	})
}

func TestScopeSatisfies(t *testing.T) {
	cases := []struct {
		have []string
		need string
		want bool
	}{
		{[]string{"read"}, "read", true},
		{[]string{"read"}, "write", false},
		{[]string{"write"}, "read", true},  // write implies read
		{[]string{"admin"}, "write", true}, // admin implies write
		{[]string{"admin"}, "read", true},  // admin implies read
		{[]string{"read", "write"}, "write", true},
		{nil, "read", false},
		{[]string{"read"}, "bogus", false}, // unknown required scope denied
	}
	for _, c := range cases {
		if got := scopeSatisfies(c.have, c.need); got != c.want {
			t.Errorf("scopeSatisfies(%v, %q) = %v, want %v", c.have, c.need, got, c.want)
		}
	}
}

func TestBearerToken(t *testing.T) {
	mk := func(h string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if h != "" {
			r.Header.Set("Authorization", h)
		}
		return r
	}
	cases := map[string]struct {
		header  string
		wantTok string
		wantOK  bool
	}{
		"valid":            {"Bearer abc.def", "abc.def", true},
		"case-insensitive": {"bearer xyz", "xyz", true},
		"missing":          {"", "", false},
		"no prefix":        {"abc.def", "", false},
		"empty token":      {"Bearer ", "", false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tok, ok := bearerToken(mk(c.header))
			if tok != c.wantTok || ok != c.wantOK {
				t.Errorf("bearerToken(%q) = (%q,%v), want (%q,%v)", c.header, tok, ok, c.wantTok, c.wantOK)
			}
		})
	}
}

func TestJWTMiddleware(t *testing.T) {
	jwtMgr := testJWTManager(t)
	a := NewAuthenticator(&fakeKeyStore{}, jwtMgr, auth.NewAPIKeyHasher("hmac_secret_at_least_32_chars_long_zzzz"), nil)

	t.Run("valid token populates context", func(t *testing.T) {
		tok, _ := jwtMgr.IssueAccessToken("merchant-1", "live", []string{"write"})
		var seen context.Context
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+tok)

		a.JWTMiddleware(okHandler(&seen)).ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rr.Code)
		}
		if MerchantID(seen) != "merchant-1" || Mode(seen) != "live" || Principal(seen) != PrincipalJWT {
			t.Errorf("context not populated: merchant=%q mode=%q principal=%q",
				MerchantID(seen), Mode(seen), Principal(seen))
		}
	})

	t.Run("missing header → 401", func(t *testing.T) {
		rr := httptest.NewRecorder()
		a.JWTMiddleware(http.NotFoundHandler()).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rr.Code)
		}
	})

	t.Run("expired token → 401", func(t *testing.T) {
		expMgr, _ := auth.NewJWTManager(
			"access_secret_at_least_32_chars_long_aaaa",
			"refresh_secret_at_least_32_chars_long_bbb",
			-time.Minute, time.Hour,
		)
		tok, _ := expMgr.IssueAccessToken("m", "test", []string{"read"})
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		a.JWTMiddleware(http.NotFoundHandler()).ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rr.Code)
		}
	})
}

func TestAPIKeyMiddleware(t *testing.T) {
	hasher := auth.NewAPIKeyHasher("hmac_secret_at_least_32_chars_long_zzzz")
	gen, _ := hasher.Generate(auth.KeyTypeSecret, "live")

	ks := &fakeKeyStore{byHash: map[string]*store.APIKeyRecord{
		gen.Hash: {
			ID:         "key-1",
			MerchantID: "merchant-7",
			Mode:       "live",
			Scope:      []string{"write"},
		},
	}}
	a := NewAuthenticator(ks, testJWTManager(t), hasher, nil)

	t.Run("valid key authenticates", func(t *testing.T) {
		var seen context.Context
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+gen.Plaintext)
		a.APIKeyMiddleware(okHandler(&seen)).ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rr.Code)
		}
		if MerchantID(seen) != "merchant-7" || Principal(seen) != PrincipalAPIKey {
			t.Errorf("context not populated: merchant=%q principal=%q", MerchantID(seen), Principal(seen))
		}
	})

	t.Run("unknown key → 401", func(t *testing.T) {
		other, _ := hasher.Generate(auth.KeyTypeSecret, "live")
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+other.Plaintext)
		a.APIKeyMiddleware(http.NotFoundHandler()).ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rr.Code)
		}
	})

	t.Run("malformed key → 401", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer not-an-api-key")
		a.APIKeyMiddleware(http.NotFoundHandler()).ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rr.Code)
		}
	})
}

func TestRevokedAPIKeyRejected(t *testing.T) {
	hasher := auth.NewAPIKeyHasher("hmac_secret_at_least_32_chars_long_zzzz")
	gen, _ := hasher.Generate(auth.KeyTypeSecret, "test")
	ks := &fakeKeyStore{byHash: map[string]*store.APIKeyRecord{
		gen.Hash: {ID: "k", MerchantID: "m", Mode: "test", Scope: []string{"read"}, RevokedAt: time.Now()},
	}}
	a := NewAuthenticator(ks, testJWTManager(t), hasher, nil)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+gen.Plaintext)
	a.APIKeyMiddleware(http.NotFoundHandler()).ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for revoked key", rr.Code)
	}
}

func TestRequireScopeAndMode(t *testing.T) {
	t.Run("RequireScope blocks insufficient", func(t *testing.T) {
		h := RequireScope("admin")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req = req.WithContext(withAuth(req.Context(), "m", "live", []string{"write"}, PrincipalAPIKey))
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rr.Code)
		}
	})

	t.Run("ModeMiddleware rejects missing mode", func(t *testing.T) {
		rr := httptest.NewRecorder()
		ModeMiddleware(http.NotFoundHandler()).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
		if rr.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rr.Code)
		}
	})

	t.Run("ModeMiddleware passes valid mode", func(t *testing.T) {
		h := ModeMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req = req.WithContext(withAuth(req.Context(), "m", "test", []string{"read"}, PrincipalAPIKey))
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rr.Code)
		}
	})
}
