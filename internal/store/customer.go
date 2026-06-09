package store

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yahya-elkady/ledger/internal/models"
	db "github.com/yahya-elkady/ledger/internal/store/db"
)

// Customer persistence errors.
var (
	// ErrCustomerNotFound is returned when no customer matches a lookup.
	ErrCustomerNotFound = errors.New("customer not found")
	// ErrInvalidCursor is returned when a pagination cursor cannot be decoded.
	ErrInvalidCursor = errors.New("invalid pagination cursor")
)

// DefaultPageSize and MaxPageSize bound list pagination.
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// CustomerStore persists merchant customers. All reads and writes are scoped to
// the owning merchant, so one merchant can never see or mutate another's data.
type CustomerStore struct {
	pool    *pgxpool.Pool
	queries *db.Queries
}

// NewCustomerStore constructs a CustomerStore from an open pool.
func NewCustomerStore(pool *pgxpool.Pool) *CustomerStore {
	return &CustomerStore{pool: pool, queries: db.New(pool)}
}

// CreateCustomer inserts a customer for a merchant.
func (s *CustomerStore) CreateCustomer(ctx context.Context, merchantID, email, name string, metadata []byte) (*models.Customer, error) {
	mid, err := textToUUID(merchantID)
	if err != nil {
		return nil, fmt.Errorf("invalid merchant id: %w", err)
	}
	row, err := s.queries.CreateCustomer(ctx, db.CreateCustomerParams{
		MerchantID: mid,
		Email:      optionalText(email),
		Name:       optionalText(name),
		Metadata:   metadata,
	})
	if err != nil {
		return nil, fmt.Errorf("creating customer: %w", err)
	}
	return customerRowToModel(row), nil
}

// GetCustomer loads one customer scoped to the merchant. Returns
// ErrCustomerNotFound if absent or owned by another merchant.
func (s *CustomerStore) GetCustomer(ctx context.Context, id, merchantID string) (*models.Customer, error) {
	cid, err := textToUUID(id)
	if err != nil {
		return nil, ErrCustomerNotFound
	}
	mid, err := textToUUID(merchantID)
	if err != nil {
		return nil, ErrCustomerNotFound
	}
	row, err := s.queries.GetCustomer(ctx, db.GetCustomerParams{ID: cid, MerchantID: mid})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCustomerNotFound
		}
		return nil, fmt.Errorf("getting customer: %w", err)
	}
	return customerRowToModel(row), nil
}

// ListCustomers returns a page of customers (newest first) and a cursor for the
// next page ("" when there are no more). limit is clamped to [1, MaxPageSize].
func (s *CustomerStore) ListCustomers(ctx context.Context, merchantID string, limit int, cursor string) ([]*models.Customer, string, error) {
	mid, err := textToUUID(merchantID)
	if err != nil {
		return nil, "", fmt.Errorf("invalid merchant id: %w", err)
	}
	limit = clampLimit(limit)
	fetch := int32(limit + 1) // one extra row signals whether a next page exists

	var rows []db.Customer
	if cursor == "" {
		rows, err = s.queries.ListCustomersFirst(ctx, db.ListCustomersFirstParams{MerchantID: mid, Limit: fetch})
	} else {
		var cur cursorKey
		cur, err = decodeCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		rows, err = s.queries.ListCustomersAfter(ctx, db.ListCustomersAfterParams{
			MerchantID: mid,
			CreatedAt:  cur.createdAt,
			ID:         cur.id,
			Limit:      fetch,
		})
	}
	if err != nil {
		return nil, "", fmt.Errorf("listing customers: %w", err)
	}

	next := ""
	if len(rows) > limit {
		last := rows[limit-1]
		rows = rows[:limit]
		next = encodeCursor(cursorKey{createdAt: last.CreatedAt, id: last.ID})
	}

	out := make([]*models.Customer, len(rows))
	for i, row := range rows {
		out[i] = customerRowToModel(row)
	}
	return out, next, nil
}

// UpdateCustomer replaces a customer's mutable fields, scoped to the merchant.
func (s *CustomerStore) UpdateCustomer(ctx context.Context, id, merchantID, email, name string, metadata []byte) (*models.Customer, error) {
	cid, err := textToUUID(id)
	if err != nil {
		return nil, ErrCustomerNotFound
	}
	mid, err := textToUUID(merchantID)
	if err != nil {
		return nil, ErrCustomerNotFound
	}
	row, err := s.queries.UpdateCustomer(ctx, db.UpdateCustomerParams{
		ID:         cid,
		MerchantID: mid,
		Email:      optionalText(email),
		Name:       optionalText(name),
		Metadata:   metadata,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCustomerNotFound
		}
		return nil, fmt.Errorf("updating customer: %w", err)
	}
	return customerRowToModel(row), nil
}

// SoftDeleteCustomer marks a customer deleted (sets deleted_at), scoped to the
// merchant. Returns ErrCustomerNotFound if absent or already deleted.
func (s *CustomerStore) SoftDeleteCustomer(ctx context.Context, id, merchantID string) error {
	cid, err := textToUUID(id)
	if err != nil {
		return ErrCustomerNotFound
	}
	mid, err := textToUUID(merchantID)
	if err != nil {
		return ErrCustomerNotFound
	}
	if _, err := s.queries.SoftDeleteCustomer(ctx, db.SoftDeleteCustomerParams{ID: cid, MerchantID: mid}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCustomerNotFound
		}
		return fmt.Errorf("deleting customer: %w", err)
	}
	return nil
}

// --- pagination cursor -----------------------------------------------------

// cursorKey is the keyset position: a (created_at, id) pair.
type cursorKey struct {
	createdAt pgtype.Timestamptz
	id        pgtype.UUID
}

// encodeCursor renders a keyset position as an opaque base64 token.
func encodeCursor(k cursorKey) string {
	raw := fmt.Sprintf("%d|%s", k.createdAt.Time.UnixNano(), uuidToText(k.id))
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeCursor parses an opaque cursor back into a keyset position.
func decodeCursor(s string) (cursorKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursorKey{}, ErrInvalidCursor
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return cursorKey{}, ErrInvalidCursor
	}
	nanos, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return cursorKey{}, ErrInvalidCursor
	}
	id, err := textToUUID(parts[1])
	if err != nil {
		return cursorKey{}, ErrInvalidCursor
	}
	return cursorKey{
		createdAt: pgtype.Timestamptz{Time: time.Unix(0, nanos).UTC(), Valid: true},
		id:        id,
	}, nil
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return DefaultPageSize
	}
	if limit > MaxPageSize {
		return MaxPageSize
	}
	return limit
}

func customerRowToModel(row db.Customer) *models.Customer {
	return &models.Customer{
		ID:         uuidToText(row.ID),
		MerchantID: uuidToText(row.MerchantID),
		Email:      derefText(row.Email),
		Name:       derefText(row.Name),
		Metadata:   row.Metadata,
		CreatedAt:  tsToTime(row.CreatedAt),
		UpdatedAt:  tsToTime(row.UpdatedAt),
	}
}

// optionalText maps "" to a NULL text column (*string), else a pointer to s.
func optionalText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// derefText maps a nullable text column to "" when NULL.
func derefText(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
