// Command smoke exercises the full stack against the real database.
//
// It runs two flows in sequence:
//
//  1. Ledger smoke — connect, create two accounts, write a balanced EntrySet,
//     read it back. This is the original Phase-1 ledger check.
//  2. Payment smoke — drive a payment through create → authorize → capture via
//     the payment.Service, then print the ledger entries each step produced.
//     This proves the Phase-2 integrated flow end to end in Postgres.
//
// The payment flow posts against the system accounts seeded by migration 0002
// (sys-cash-in-transit, sys-authorization-hold, sys-settled-funds). Apply
// migrations 0001 and 0002 before running.
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
	"github.com/yahya-elkady/ledger/internal/payment"
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

	ledgerStore := store.New(pool)

	// Unique suffix so repeated runs don't collide on primary keys.
	run := time.Now().UnixNano()

	runLedgerSmoke(ctx, ledgerStore, run)
	fmt.Println()
	runPaymentSmoke(ctx, ledgerStore, pool, run)

	fmt.Println("smoke test OK")
}

// runLedgerSmoke writes and reads back a single balanced sale entry set —
// the original ledger-layer check.
func runLedgerSmoke(ctx context.Context, s *store.LedgerStore, run int64) {
	cashID := fmt.Sprintf("acct-cash-%d", run)
	revID := fmt.Sprintf("acct-rev-%d", run)

	cash := ledger.Account{ID: cashID, Name: "Cash", Currency: "USD", Type: ledger.AccountTypeAsset}
	revenue := ledger.Account{ID: revID, Name: "Sales Revenue", Currency: "USD", Type: ledger.AccountTypeRevenue}

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
		{ID: fmt.Sprintf("entry-debit-%d", run), AccountID: cashID, Amount: amount, Direction: ledger.Debit, Memo: "cash received for sale", CreatedAt: now},
		{ID: fmt.Sprintf("entry-credit-%d", run), AccountID: revID, Amount: amount, Direction: ledger.Credit, Memo: "revenue recognized", CreatedAt: now},
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
	printEntries(got)
	if len(got) != 2 {
		log.Fatalf("expected 2 entries, got %d", len(got))
	}
}

// runPaymentSmoke drives a payment through the full authorize → capture flow
// via payment.Service and prints the ledger entries produced at each step.
func runPaymentSmoke(ctx context.Context, ledgerStore *store.LedgerStore, pool *store.Pool, run int64) {
	fmt.Println("--- payment flow ---")

	paymentStore := store.NewPaymentStore(pool)
	provider := &payment.FakeProvider{AuthorizeRef: fmt.Sprintf("provider-ref-%d", run)}
	svc := payment.NewService(paymentStore, ledgerStore, provider)

	paymentID := fmt.Sprintf("pay-%d", run)
	idemKey := fmt.Sprintf("idem-%d", run)
	amount := ledger.MustMoney(2500, "USD") // $25.00

	// Create — a new pending payment (no ledger entries yet).
	p, err := svc.CreatePayment(ctx, paymentID, amount, idemKey)
	if err != nil {
		log.Fatalf("create payment: %v", err)
	}
	fmt.Printf("created payment %s amount=%s status=%s\n", p.ID, p.Amount, p.Status)

	// Idempotency check: same key returns the same payment, no new row.
	again, err := svc.CreatePayment(ctx, paymentID+"-dup", amount, idemKey)
	if err != nil {
		log.Fatalf("idempotent create: %v", err)
	}
	if again.ID != p.ID {
		log.Fatalf("idempotency broken: got id %s, want %s", again.ID, p.ID)
	}
	fmt.Printf("idempotent re-create returned existing payment %s (status=%s)\n", again.ID, again.Status)

	// Authorize — provider holds funds; ledger records the hold.
	p, err = svc.Authorize(ctx, paymentID)
	if err != nil {
		log.Fatalf("authorize: %v", err)
	}
	fmt.Printf("authorized payment %s status=%s providerRef=%s\n", p.ID, p.Status, p.ProviderRef)
	printTxn(ctx, ledgerStore, paymentID+":authorize")

	// Capture — funds move from held to settled.
	p, err = svc.Capture(ctx, paymentID)
	if err != nil {
		log.Fatalf("capture: %v", err)
	}
	fmt.Printf("captured payment %s status=%s\n", p.ID, p.Status)
	printTxn(ctx, ledgerStore, paymentID+":capture")

	if p.Status != payment.StatusCaptured {
		log.Fatalf("expected captured, got %s", p.Status)
	}
}

// printTxn reads back and prints the entries for one payment transaction.
func printTxn(ctx context.Context, s *store.LedgerStore, txID string) {
	got, err := s.GetEntriesByTransaction(ctx, txID)
	if err != nil {
		log.Fatalf("read entries for %s: %v", txID, err)
	}
	fmt.Printf("  ledger transaction %s:\n", txID)
	printEntries(got)
}

// printEntries renders a slice of entries in a fixed-width table.
func printEntries(entries []ledger.Entry) {
	for _, e := range entries {
		fmt.Printf("    %-26s account=%-24s %-6s %s  memo=%q\n",
			e.ID, e.AccountID, e.Direction, e.Amount, e.Memo)
	}
}
