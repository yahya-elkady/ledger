package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yahya-elkady/ledger/internal/models"
	db "github.com/yahya-elkady/ledger/internal/store/db"
)

// Merchant persistence errors.
var (
	// ErrMerchantNotFound is returned when no merchant matches a lookup.
	ErrMerchantNotFound = errors.New("merchant not found")
	// ErrEmailTaken is returned when registering an email that already exists
	// (the merchants.email UNIQUE constraint).
	ErrEmailTaken = errors.New("email already registered")
)

// uniqueViolation is the PostgreSQL SQLSTATE for a unique_violation.
const uniqueViolation = "23505"

// MerchantStore persists merchant accounts. The bcrypt password hash is passed
// in by the caller (the auth package owns hashing) and is never returned in the
// public models.Merchant — it is only handed back, separately, for login.
type MerchantStore struct {
	pool    *pgxpool.Pool
	queries *db.Queries
}

// NewMerchantStore constructs a MerchantStore from an open pool.
func NewMerchantStore(pool *pgxpool.Pool) *MerchantStore {
	return &MerchantStore{pool: pool, queries: db.New(pool)}
}

// CreateMerchant inserts a new merchant and returns its public view. A duplicate
// email is reported as ErrEmailTaken.
func (s *MerchantStore) CreateMerchant(ctx context.Context, email, passwordHash, businessName, mode string) (*models.Merchant, error) {
	row, err := s.queries.CreateMerchant(ctx, db.CreateMerchantParams{
		Email:        email,
		PasswordHash: passwordHash,
		BusinessName: businessName,
		Mode:         mode,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return nil, ErrEmailTaken
		}
		return nil, fmt.Errorf("creating merchant: %w", err)
	}
	return merchantRowToModel(row), nil
}

// GetMerchantByEmail loads a merchant by email for login, returning its public
// view alongside the stored password hash (needed only to verify the password).
// Returns ErrMerchantNotFound if absent.
func (s *MerchantStore) GetMerchantByEmail(ctx context.Context, email string) (*models.Merchant, string, error) {
	row, err := s.queries.GetMerchantByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", ErrMerchantNotFound
		}
		return nil, "", fmt.Errorf("getting merchant by email: %w", err)
	}
	return merchantRowToModel(row), row.PasswordHash, nil
}

// GetMerchantByID loads a merchant by id. Returns ErrMerchantNotFound if absent.
func (s *MerchantStore) GetMerchantByID(ctx context.Context, id string) (*models.Merchant, error) {
	uid, err := textToUUID(id)
	if err != nil {
		return nil, ErrMerchantNotFound
	}
	row, err := s.queries.GetMerchantByID(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrMerchantNotFound
		}
		return nil, fmt.Errorf("getting merchant by id: %w", err)
	}
	return merchantRowToModel(row), nil
}

func merchantRowToModel(row db.Merchant) *models.Merchant {
	return &models.Merchant{
		ID:           uuidToText(row.ID),
		Email:        row.Email,
		BusinessName: row.BusinessName,
		Mode:         row.Mode,
		CreatedAt:    tsToTime(row.CreatedAt),
		UpdatedAt:    tsToTime(row.UpdatedAt),
	}
}
