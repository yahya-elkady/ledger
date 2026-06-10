// Command seed populates the database with test-mode fixtures for local
// development and for the test/live isolation checks (Phase 9).
//
// It creates, all in test mode:
//   - one test merchant (dashboard login)
//   - one test API key (the plaintext is printed ONCE — it is never stored)
//   - one customer
//   - a few charges referencing Stripe test card tokens
//
// Everything is written with mode='test', so it can never surface through a
// live-mode API key or JWT. Stripe's published test card tokens (pm_card_visa,
// tok_chargeDeclined, …) are public fixtures, not secrets — they are recorded
// in charge metadata; no live processor call is made.
//
// Run against a valid DATABASE_URL + API_KEY_HMAC_SECRET (loaded from .env):
//
//	make seed-test    # or: go run ./cmd/seed
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/yahya-elkady/ledger/internal/auth"
	"github.com/yahya-elkady/ledger/internal/config"
	"github.com/yahya-elkady/ledger/internal/store"
)

// seedMode is fixed: this tool only ever writes test-mode data.
const seedMode = "test"

func main() {
	ctx := context.Background()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	pool, err := store.Connect(ctx)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	merchants := store.NewMerchantStore(pool)
	authStore := store.NewAuthStore(pool)
	customers := store.NewCustomerStore(pool)
	charges := store.NewChargeStore(pool)
	hasher := auth.NewAPIKeyHasher(cfg.APIKeyHMACSecret)

	// Unique email suffix so repeated runs don't collide on the UNIQUE(email).
	run := time.Now().UnixNano()
	email := fmt.Sprintf("seed+%d@test.local", run)

	// 1. Test merchant.
	pwHash, err := auth.HashPassword("seed-password-123")
	if err != nil {
		log.Fatalf("hash password: %v", err)
	}
	m, err := merchants.CreateMerchant(ctx, email, pwHash, "Seed Test Shop", seedMode)
	if err != nil {
		log.Fatalf("create merchant: %v", err)
	}
	fmt.Printf("merchant: %s  (email %s, password seed-password-123, mode %s)\n", m.ID, email, seedMode)

	// 2. Test API key — print the plaintext once; only the hash is persisted.
	gen, err := hasher.Generate(auth.KeyTypeSecret, seedMode)
	if err != nil {
		log.Fatalf("generate api key: %v", err)
	}
	if _, err := authStore.SaveAPIKey(ctx, store.APIKeyRecord{
		MerchantID: m.ID,
		Name:       "seed test key",
		KeyHash:    gen.Hash,
		KeyPrefix:  gen.Prefix,
		Type:       string(auth.KeyTypeSecret),
		Mode:       seedMode,
		Scope:      []string{"admin"},
	}); err != nil {
		log.Fatalf("save api key: %v", err)
	}
	fmt.Printf("api key (shown once): %s\n", gen.Plaintext)

	// 3. Customer.
	cust, err := customers.CreateCustomer(ctx, m.ID, "buyer@test.local", "Test Buyer", nil)
	if err != nil {
		log.Fatalf("create customer: %v", err)
	}
	fmt.Printf("customer: %s\n", cust.ID)

	// 4. Charges referencing Stripe test card tokens (public fixtures).
	seedCharges := []struct {
		amount    int64
		currency  string
		status    string
		token     string // Stripe test token, recorded in metadata
		failCode  string
		failMsg   string
		procChgID string
	}{
		{2000, "USD", "succeeded", "pm_card_visa", "", "", fmt.Sprintf("ch_seed_%d_1", run)},
		{500, "EUR", "succeeded", "pm_card_mastercard", "", "", fmt.Sprintf("ch_seed_%d_2", run)},
		{1500, "USD", "failed", "tok_chargeDeclined", "card_declined", "Your card was declined.", ""},
	}
	for i, sc := range seedCharges {
		_, err := charges.CreateCharge(ctx, store.NewCharge{
			MerchantID:        m.ID,
			CustomerID:        cust.ID,
			Amount:            sc.amount,
			Currency:          sc.currency,
			Status:            sc.status,
			Processor:         "stripe",
			ProcessorChargeID: sc.procChgID,
			IdempotencyKey:    fmt.Sprintf("seed-%d-%d", run, i),
			Mode:              seedMode,
			FailureCode:       sc.failCode,
			FailureMessage:    sc.failMsg,
			Metadata:          []byte(fmt.Sprintf(`{"seed":true,"stripe_test_token":%q}`, sc.token)),
		})
		if err != nil {
			log.Fatalf("create charge %d: %v", i, err)
		}
	}
	fmt.Printf("charges: %d test-mode charges created\n", len(seedCharges))

	fmt.Println("seed OK — all data is mode='test' and isolated from live")
}
