package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/yahya-elkady/ledger/internal/store/db"
	"github.com/yahya-elkady/ledger/internal/ledger"
)

// LedgerStore writes domain types to the database.
// It owns the translation between your domain EntrySet and the
// sqlc-generated DB params — keeping that mapping in one place.
type LedgerStore struct {
	pool    *pgxpool.Pool
	queries *db.Queries
}

// New constructs a LedgerStore from an open connection pool.
func New(pool *pgxpool.Pool) *LedgerStore {
	return &LedgerStore{
		pool:    pool,
		queries: db.New(pool),
	}
}

// CreateAccount persists a new account to the database.
// Returns an error if the account already exists or fails validation.
func (s *LedgerStore) CreateAccount(ctx context.Context, a ledger.Account) error {
	if err := a.Validate(); err != nil {
		return fmt.Errorf("invalid account: %w", err)
	}

	_, err := s.queries.CreateAccount(ctx, db.CreateAccountParams{
		ID:       a.ID,
		Name:     a.Name,
		Currency: a.Currency,
		Type:     string(a.Type),
	})
	if err != nil {
		return fmt.Errorf("creating account %s: %w", a.ID, err)
	}
	return nil
}

// Store writes all entries in an EntrySet to the database atomically.
// All entries land in a single Postgres transaction — either every entry
// is written, or none are. The balance invariant is validated before
// any database work begins.
//
// transactionID groups the entries so they can be queried together later
// (e.g. "show me all entries for payment X").
func (s *LedgerStore) Store(ctx context.Context, transactionID string, es ledger.EntrySet) error {
	if transactionID == "" {
		return fmt.Errorf("transactionID is required")
	}

	// Begin a Postgres transaction.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{
		// Read committed is the default isolation level and sufficient here:
		// each write is independent and the balance invariant is already
		// enforced by EntrySet.Validate() in Go before we get here.
		IsoLevel: pgx.ReadCommitted,
	})
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}

	// Ensure the transaction is always resolved.
	// If we return early with an error, Rollback undoes any partial writes.
	// If we reach Commit below, this becomes a no-op.
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	qtx := s.queries.WithTx(tx)

	// Insert every entry inside the same transaction.
	for _, entry := range es.Entries() {
		_, err := qtx.CreateEntry(ctx, db.CreateEntryParams{
			ID:            entry.ID,
			AccountID:     entry.AccountID,
			Amount:        entry.Amount.Amount,
			Direction:     int16(entry.Direction),
			Currency:      entry.Amount.Currency,
			Memo:          entry.Memo,
			TransactionID: transactionID,
			// created_at is set by the database via now() (see ledger.sql).
		})
		if err != nil {
			// Rollback is handled by the deferred call above.
			return fmt.Errorf("inserting entry %s: %w", entry.ID, err)
		}
	}

	// Commit only after every entry has been inserted successfully.
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	return nil
}

// GetEntriesByTransaction retrieves all entries for a given transaction ID,
// converting the DB rows back into domain Entry values.
func (s *LedgerStore) GetEntriesByTransaction(ctx context.Context, transactionID string) ([]ledger.Entry, error) {
	rows, err := s.queries.GetEntriesByTransaction(ctx, transactionID)
	if err != nil {
		return nil, fmt.Errorf("getting entries for transaction %s: %w", transactionID, err)
	}
	return rowsToEntries(rows), nil
}

// GetEntriesByAccount retrieves all entries for a given account ID.
func (s *LedgerStore) GetEntriesByAccount(ctx context.Context, accountID string) ([]ledger.Entry, error) {
	rows, err := s.queries.GetEntriesByAccount(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("getting entries for account %s: %w", accountID, err)
	}
	return rowsToEntries(rows), nil
}

// rowsToEntries converts a slice of DB rows into domain Entry values.
// This is the translation layer going the other direction — DB → domain.
func rowsToEntries(rows []db.Entry) []ledger.Entry {
	entries := make([]ledger.Entry, len(rows))
	for i, row := range rows {
		entries[i] = ledger.Entry{
			ID:        row.ID,
			AccountID: row.AccountID,
			Amount:    ledger.MustMoney(row.Amount, row.Currency),
			Direction: ledger.Direction(row.Direction),
			Memo:      row.Memo,
			CreatedAt: row.CreatedAt.Time,
		}
	}
	return entries
}