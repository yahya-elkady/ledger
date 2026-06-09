package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/yahya-elkady/ledger/internal/store/db"
)

// Auth persistence errors. Callers branch on these with errors.Is.
var (
	// ErrAPIKeyNotFound is returned when no api_keys row matches a lookup.
	ErrAPIKeyNotFound = errors.New("api key not found")
	// ErrRefreshTokenNotFound is returned when no refresh_tokens row matches,
	// or when a rotation target has already been revoked.
	ErrRefreshTokenNotFound = errors.New("refresh token not found")
)

// APIKeyRecord is the domain view of a stored API key. It deliberately omits the
// plaintext (which is never stored) and exposes only what callers need to make
// authz decisions. KeyHash is the HMAC hash (used as the Redis cache key and for
// cache invalidation on revoke), not a secret.
type APIKeyRecord struct {
	ID         string
	MerchantID string
	Name       string
	KeyHash    string
	KeyPrefix  string
	Type       string
	Mode       string
	Scope      []string
	LastUsedAt time.Time
	ExpiresAt  time.Time // zero => never expires
	CreatedAt  time.Time
	RevokedAt  time.Time // zero => active
}

// IsRevoked reports whether the key has been revoked.
func (r *APIKeyRecord) IsRevoked() bool { return !r.RevokedAt.IsZero() }

// IsExpired reports whether the key has an expiry that is now in the past.
func (r *APIKeyRecord) IsExpired(now time.Time) bool {
	return !r.ExpiresAt.IsZero() && now.After(r.ExpiresAt)
}

// IsActive reports whether the key may currently authenticate a request.
func (r *APIKeyRecord) IsActive(now time.Time) bool {
	return !r.IsRevoked() && !r.IsExpired(now)
}

// RefreshTokenRecord is the data persisted for one refresh token. Only the hash
// of the token is stored — never the token itself.
type RefreshTokenRecord struct {
	MerchantID string
	TokenHash  string
	JTI        string
	ExpiresAt  time.Time
}

// AuthStore persists API keys and refresh tokens. Like LedgerStore and
// PaymentStore it is pure persistence: callers pass already-hashed values
// (the auth package owns hashing) and it never sees plaintext secrets.
type AuthStore struct {
	pool    *pgxpool.Pool
	queries *db.Queries
}

// NewAuthStore constructs an AuthStore from an open pool.
func NewAuthStore(pool *pgxpool.Pool) *AuthStore {
	return &AuthStore{pool: pool, queries: db.New(pool)}
}

// --- API keys --------------------------------------------------------------

// SaveAPIKey inserts a new API key row from an already-hashed key and returns
// the persisted record.
func (s *AuthStore) SaveAPIKey(ctx context.Context, rec APIKeyRecord) (*APIKeyRecord, error) {
	merchantID, err := textToUUID(rec.MerchantID)
	if err != nil {
		return nil, fmt.Errorf("invalid merchant id: %w", err)
	}
	row, err := s.queries.CreateAPIKey(ctx, db.CreateAPIKeyParams{
		MerchantID: merchantID,
		Name:       rec.Name,
		KeyHash:    rec.KeyHash,
		KeyPrefix:  rec.KeyPrefix,
		Type:       rec.Type,
		Mode:       rec.Mode,
		Scope:      rec.Scope,
		ExpiresAt:  timeToTS(rec.ExpiresAt),
	})
	if err != nil {
		return nil, fmt.Errorf("creating api key: %w", err)
	}
	return apiKeyRowToRecord(row), nil
}

// ListAPIKeysByMerchant returns a merchant's active (non-revoked) API keys,
// newest first.
func (s *AuthStore) ListAPIKeysByMerchant(ctx context.Context, merchantID string) ([]*APIKeyRecord, error) {
	mid, err := textToUUID(merchantID)
	if err != nil {
		return nil, fmt.Errorf("invalid merchant id: %w", err)
	}
	rows, err := s.queries.ListAPIKeysByMerchant(ctx, mid)
	if err != nil {
		return nil, fmt.Errorf("listing api keys: %w", err)
	}
	out := make([]*APIKeyRecord, len(rows))
	for i, row := range rows {
		out[i] = apiKeyRowToRecord(row)
	}
	return out, nil
}

// GetAPIKeyByID loads a single API key by id, scoped to the owning merchant.
// Returns ErrAPIKeyNotFound if absent or owned by another merchant.
func (s *AuthStore) GetAPIKeyByID(ctx context.Context, id, merchantID string) (*APIKeyRecord, error) {
	kid, err := textToUUID(id)
	if err != nil {
		return nil, ErrAPIKeyNotFound
	}
	row, err := s.queries.GetAPIKeyByID(ctx, kid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrAPIKeyNotFound
		}
		return nil, fmt.Errorf("getting api key by id: %w", err)
	}
	rec := apiKeyRowToRecord(row)
	if rec.MerchantID != merchantID {
		// Don't leak the existence of another merchant's key.
		return nil, ErrAPIKeyNotFound
	}
	return rec, nil
}

// GetAPIKeyByHash loads an API key by its HMAC hash. Returns ErrAPIKeyNotFound
// if absent.
func (s *AuthStore) GetAPIKeyByHash(ctx context.Context, hash string) (*APIKeyRecord, error) {
	row, err := s.queries.GetAPIKeyByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrAPIKeyNotFound
		}
		return nil, fmt.Errorf("getting api key by hash: %w", err)
	}
	return apiKeyRowToRecord(row), nil
}

// RevokeAPIKey soft-deletes a key (sets revoked_at) for the owning merchant and
// returns the revoked record (whose KeyHash the caller uses to evict any cache
// entry). Returns ErrAPIKeyNotFound if it does not exist, is already revoked, or
// belongs to another merchant.
func (s *AuthStore) RevokeAPIKey(ctx context.Context, keyID, merchantID string) (*APIKeyRecord, error) {
	id, err := textToUUID(keyID)
	if err != nil {
		return nil, fmt.Errorf("invalid key id: %w", err)
	}
	mid, err := textToUUID(merchantID)
	if err != nil {
		return nil, fmt.Errorf("invalid merchant id: %w", err)
	}
	row, err := s.queries.RevokeAPIKey(ctx, db.RevokeAPIKeyParams{ID: id, MerchantID: mid})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrAPIKeyNotFound
		}
		return nil, fmt.Errorf("revoking api key: %w", err)
	}
	return apiKeyRowToRecord(row), nil
}

// --- refresh tokens --------------------------------------------------------

// SaveRefreshToken inserts a refresh token (hash + jti + expiry).
func (s *AuthStore) SaveRefreshToken(ctx context.Context, rec RefreshTokenRecord) error {
	merchantID, err := textToUUID(rec.MerchantID)
	if err != nil {
		return fmt.Errorf("invalid merchant id: %w", err)
	}
	_, err = s.queries.CreateRefreshToken(ctx, db.CreateRefreshTokenParams{
		MerchantID: merchantID,
		TokenHash:  rec.TokenHash,
		Jti:        rec.JTI,
		ExpiresAt:  timeToTS(rec.ExpiresAt),
	})
	if err != nil {
		return fmt.Errorf("creating refresh token: %w", err)
	}
	return nil
}

// RevokeRefreshTokenByJTI revokes a single refresh token by its jti.
func (s *AuthStore) RevokeRefreshTokenByJTI(ctx context.Context, jti string) error {
	_, err := s.queries.RevokeRefreshTokenByJTI(ctx, jti)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRefreshTokenNotFound
		}
		return fmt.Errorf("revoking refresh token: %w", err)
	}
	return nil
}

// RotateRefreshToken atomically revokes the old refresh token (looked up by its
// hash) and inserts the new one, in a single transaction. Either both happen or
// neither — so a crash mid-rotation can never leave two live tokens or zero.
// Returns ErrRefreshTokenNotFound if the old token is absent or already revoked.
func (s *AuthStore) RotateRefreshToken(ctx context.Context, oldHash string, next RefreshTokenRecord) error {
	merchantID, err := textToUUID(next.MerchantID)
	if err != nil {
		return fmt.Errorf("invalid merchant id: %w", err)
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("beginning rotation transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.queries.WithTx(tx)

	// Revoke the presented token. If it is missing or already revoked, the
	// rotation is illegal (possible token reuse) and we abort.
	if _, err := qtx.RevokeRefreshTokenByHash(ctx, oldHash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRefreshTokenNotFound
		}
		return fmt.Errorf("revoking old refresh token: %w", err)
	}

	if _, err := qtx.CreateRefreshToken(ctx, db.CreateRefreshTokenParams{
		MerchantID: merchantID,
		TokenHash:  next.TokenHash,
		Jti:        next.JTI,
		ExpiresAt:  timeToTS(next.ExpiresAt),
	}); err != nil {
		return fmt.Errorf("inserting new refresh token: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing rotation: %w", err)
	}
	return nil
}

// --- mapping helpers -------------------------------------------------------

func apiKeyRowToRecord(row db.ApiKey) *APIKeyRecord {
	return &APIKeyRecord{
		ID:         uuidToText(row.ID),
		MerchantID: uuidToText(row.MerchantID),
		Name:       row.Name,
		KeyHash:    row.KeyHash,
		KeyPrefix:  row.KeyPrefix,
		Type:       row.Type,
		Mode:       row.Mode,
		Scope:      row.Scope,
		LastUsedAt: tsToTime(row.LastUsedAt),
		ExpiresAt:  tsToTime(row.ExpiresAt),
		CreatedAt:  tsToTime(row.CreatedAt),
		RevokedAt:  tsToTime(row.RevokedAt),
	}
}

// textToUUID parses a canonical UUID string into a pgtype.UUID.
func textToUUID(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return pgtype.UUID{}, err
	}
	return u, nil
}

// uuidToText renders a pgtype.UUID as a canonical string ("" if NULL).
func uuidToText(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// timeToTS converts a time.Time into a pgtype.Timestamptz, treating the zero
// time as SQL NULL.
func timeToTS(t time.Time) pgtype.Timestamptz {
	if t.IsZero() {
		return pgtype.Timestamptz{Valid: false}
	}
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// tsToTime converts a pgtype.Timestamptz into a time.Time (zero if NULL).
func tsToTime(ts pgtype.Timestamptz) time.Time {
	if !ts.Valid {
		return time.Time{}
	}
	return ts.Time
}
