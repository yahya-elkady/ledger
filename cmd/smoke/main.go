// Command smoke exercises the full ledger flow against the real database:
// connect → create two accounts → write a balanced EntrySet → read it back.
//
// Run with a valid DATABASE_URL (loaded from .env by store.Connect):
//
//	go run ./cmd/smoke
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/yahya-elkady/ledger/internal/ledger"
	"github.com/yahya-elkady/ledger/internal/store"
)

func main() {
	ctx := context.Background()

	// store.Connect loads .env and reads DATABASE_URL.
	pool, err := store.Connect(ctx)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	s := store.New(pool)

	// Unique suffix so repeated runs don't collide on primary keys.
	run := time.Now().UnixNano()

	cashID := fmt.Sprintf("acct-cash-%d", run)
	revID := fmt.Sprintf("acct-rev-%d", run)

	cash := ledger.Account{
		ID:       cashID,
		Name:     "Cash",
		Currency: "USD",
		Type:     ledger.AccountTypeAsset,
	}
	revenue := ledger.Account{
		ID:       revID,
		Name:     "Sales Revenue",
		Currency: "USD",
		Type:     ledger.AccountTypeRevenue,
	}

	if err := s.CreateAccount(ctx, cash); err != nil {
		log.Fatalf("create cash account: %v", err)
	}
	if err := s.CreateAccount(ctx, revenue); err != nil {
		log.Fatalf("create revenue account: %v", err)
	}
	fmt.Printf("created accounts %s (asset) and %s (revenue)\n", cashID, revID)

	// A $10.99 USD sale: debit cash (asset increases), credit revenue.
	amount := ledger.MustMoney(1099, "USD")
	now := time.Now()

	entries := []ledger.Entry{
		{
			ID:        fmt.Sprintf("entry-debit-%d", run),
			AccountID: cashID,
			Amount:    amount,
			Direction: ledger.Debit,
			Memo:      "cash received for sale",
			CreatedAt: now,
		},
		{
			ID:        fmt.Sprintf("entry-credit-%d", run),
			AccountID: revID,
			Amount:    amount,
			Direction: ledger.Credit,
			Memo:      "revenue recognized",
			CreatedAt: now,
		},
	}

	es, err := ledger.NewEntrySet(entries)
	if err != nil {
		log.Fatalf("build entry set: %v", err)
	}

	txID := fmt.Sprintf("txn-%d", run)
	if err := s.Store(ctx, txID, es); err != nil {
		log.Fatalf("store entry set: %v", err)
	}
	fmt.Printf("wrote balanced entry set under transaction %s\n", txID)

	got, err := s.GetEntriesByTransaction(ctx, txID)
	if err != nil {
		log.Fatalf("read entries: %v", err)
	}

	fmt.Printf("read back %d entries:\n", len(got))
	for _, e := range got {
		fmt.Printf("  %-22s account=%-20s %-6s %s  memo=%q  at=%s\n",
			e.ID, e.AccountID, e.Direction, e.Amount, e.Memo,
			e.CreatedAt.Format(time.RFC3339),
		)
	}

	if len(got) != 2 {
		log.Fatalf("expected 2 entries, got %d", len(got))
	}
	fmt.Println("smoke test OK")
}
