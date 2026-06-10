package handlers_test

import (
	"context"
	"sync"
	"time"

	"github.com/yahya-elkady/ledger/internal/models"
	"github.com/yahya-elkady/ledger/internal/store"
)

// fakeMerchants is an in-memory MerchantStore.
type fakeMerchants struct {
	mu      sync.Mutex
	byEmail map[string]*models.Merchant
	byID    map[string]*models.Merchant
	hashes  map[string]string // email -> password hash
	seq     int
}

func newFakeMerchants() *fakeMerchants {
	return &fakeMerchants{byEmail: map[string]*models.Merchant{}, byID: map[string]*models.Merchant{}, hashes: map[string]string{}}
}

func (f *fakeMerchants) CreateMerchant(_ context.Context, email, passwordHash, businessName, mode string) (*models.Merchant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.byEmail[email]; ok {
		return nil, store.ErrEmailTaken
	}
	f.seq++
	m := &models.Merchant{
		ID:           "11111111-1111-1111-1111-" + pad12(f.seq),
		Email:        email,
		BusinessName: businessName,
		Mode:         mode,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	f.byEmail[email] = m
	f.byID[m.ID] = m
	f.hashes[email] = passwordHash
	return m, nil
}

func (f *fakeMerchants) GetMerchantByEmail(_ context.Context, email string) (*models.Merchant, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.byEmail[email]
	if !ok {
		return nil, "", store.ErrMerchantNotFound
	}
	return m, f.hashes[email], nil
}

func (f *fakeMerchants) GetMerchantByID(_ context.Context, id string) (*models.Merchant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.byID[id]
	if !ok {
		return nil, store.ErrMerchantNotFound
	}
	return m, nil
}

// fakeTokens is an in-memory RefreshTokenStore keyed by token hash.
type fakeTokens struct {
	mu      sync.Mutex
	byHash  map[string]store.RefreshTokenRecord
	byJTI   map[string]string // jti -> hash
	revoked map[string]bool   // hash -> revoked
}

func newFakeTokens() *fakeTokens {
	return &fakeTokens{byHash: map[string]store.RefreshTokenRecord{}, byJTI: map[string]string{}, revoked: map[string]bool{}}
}

func (f *fakeTokens) SaveRefreshToken(_ context.Context, rec store.RefreshTokenRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byHash[rec.TokenHash] = rec
	f.byJTI[rec.JTI] = rec.TokenHash
	return nil
}

func (f *fakeTokens) RotateRefreshToken(_ context.Context, oldHash string, next store.RefreshTokenRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.byHash[oldHash]
	if !ok || f.revoked[oldHash] {
		return store.ErrRefreshTokenNotFound
	}
	f.revoked[oldHash] = true
	f.byHash[next.TokenHash] = next
	f.byJTI[next.JTI] = next.TokenHash
	return nil
}

func (f *fakeTokens) RevokeRefreshTokenByJTI(_ context.Context, jti string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	hash, ok := f.byJTI[jti]
	if !ok {
		return store.ErrRefreshTokenNotFound
	}
	f.revoked[hash] = true
	return nil
}

// fakeAPIKeys is an in-memory APIKeyStore.
type fakeAPIKeys struct {
	mu   sync.Mutex
	byID map[string]*store.APIKeyRecord
	seq  int
}

func newFakeAPIKeys() *fakeAPIKeys {
	return &fakeAPIKeys{byID: map[string]*store.APIKeyRecord{}}
}

func (f *fakeAPIKeys) SaveAPIKey(_ context.Context, rec store.APIKeyRecord) (*store.APIKeyRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	rec.ID = "22222222-2222-2222-2222-" + pad12(f.seq)
	rec.CreatedAt = time.Now()
	cp := rec
	f.byID[rec.ID] = &cp
	return &cp, nil
}

func (f *fakeAPIKeys) ListAPIKeysByMerchant(_ context.Context, merchantID string) ([]*store.APIKeyRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*store.APIKeyRecord
	for _, rec := range f.byID {
		if rec.MerchantID == merchantID && rec.RevokedAt.IsZero() {
			cp := *rec
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (f *fakeAPIKeys) GetAPIKeyByID(_ context.Context, id, merchantID string) (*store.APIKeyRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.byID[id]
	if !ok || rec.MerchantID != merchantID {
		return nil, store.ErrAPIKeyNotFound
	}
	cp := *rec
	return &cp, nil
}

func (f *fakeAPIKeys) RevokeAPIKey(_ context.Context, keyID, merchantID string) (*store.APIKeyRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.byID[keyID]
	if !ok || rec.MerchantID != merchantID || !rec.RevokedAt.IsZero() {
		return nil, store.ErrAPIKeyNotFound
	}
	rec.RevokedAt = time.Now()
	cp := *rec
	return &cp, nil
}

// fakeKeyCache records API-key cache evictions (revocation must evict).
type fakeKeyCache struct {
	mu      sync.Mutex
	evicted []string
}

func (f *fakeKeyCache) InvalidateAPIKeyCache(_ context.Context, keyHash string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evicted = append(f.evicted, keyHash)
}

func (f *fakeKeyCache) evictions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.evicted...)
}

// fakeCustomers is an in-memory CustomerStore (no pagination cursor logic — it
// returns everything for the merchant, which is enough for these tests).
type fakeCustomers struct {
	mu   sync.Mutex
	byID map[string]*models.Customer
	seq  int
}

func newFakeCustomers() *fakeCustomers {
	return &fakeCustomers{byID: map[string]*models.Customer{}}
}

func (f *fakeCustomers) CreateCustomer(_ context.Context, merchantID, email, name string, metadata []byte) (*models.Customer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	c := &models.Customer{
		ID:         "33333333-3333-3333-3333-" + pad12(f.seq),
		MerchantID: merchantID,
		Email:      email,
		Name:       name,
		Metadata:   metadata,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	f.byID[c.ID] = c
	return c, nil
}

func (f *fakeCustomers) GetCustomer(_ context.Context, id, merchantID string) (*models.Customer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.byID[id]
	if !ok || c.MerchantID != merchantID {
		return nil, store.ErrCustomerNotFound
	}
	return c, nil
}

func (f *fakeCustomers) ListCustomers(_ context.Context, merchantID string, limit int, cursor string) ([]*models.Customer, string, error) {
	if cursor == "bad" {
		return nil, "", store.ErrInvalidCursor
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*models.Customer
	for _, c := range f.byID {
		if c.MerchantID == merchantID {
			out = append(out, c)
		}
	}
	return out, "", nil
}

func (f *fakeCustomers) UpdateCustomer(_ context.Context, id, merchantID, email, name string, metadata []byte) (*models.Customer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.byID[id]
	if !ok || c.MerchantID != merchantID {
		return nil, store.ErrCustomerNotFound
	}
	c.Email, c.Name, c.Metadata, c.UpdatedAt = email, name, metadata, time.Now()
	return c, nil
}

func (f *fakeCustomers) SoftDeleteCustomer(_ context.Context, id, merchantID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.byID[id]
	if !ok || c.MerchantID != merchantID {
		return store.ErrCustomerNotFound
	}
	delete(f.byID, id)
	return nil
}

// fakeCharges is an in-memory ChargeStore.
type fakeCharges struct {
	mu     sync.Mutex
	byID   map[string]*models.Charge
	byIdem map[string]bool
	seq    int
}

func newFakeCharges() *fakeCharges {
	return &fakeCharges{byID: map[string]*models.Charge{}, byIdem: map[string]bool{}}
}

func (f *fakeCharges) CreateCharge(_ context.Context, c store.NewCharge) (*models.Charge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.IdempotencyKey != "" && f.byIdem[c.IdempotencyKey] {
		return nil, store.ErrIdempotencyConflict
	}
	f.seq++
	m := &models.Charge{
		ID:                "44444444-4444-4444-4444-" + pad12(f.seq),
		MerchantID:        c.MerchantID,
		CustomerID:        c.CustomerID,
		PaymentMethodID:   c.PaymentMethodID,
		Amount:            c.Amount,
		Currency:          c.Currency,
		Status:            c.Status,
		Processor:         c.Processor,
		ProcessorChargeID: c.ProcessorChargeID,
		Mode:              c.Mode,
		FailureCode:       c.FailureCode,
		FailureMessage:    c.FailureMessage,
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}
	f.byID[m.ID] = m
	if c.IdempotencyKey != "" {
		f.byIdem[c.IdempotencyKey] = true
	}
	return m, nil
}

func (f *fakeCharges) GetCharge(_ context.Context, id, merchantID, mode string) (*models.Charge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.byID[id]
	if !ok || c.MerchantID != merchantID || c.Mode != mode {
		return nil, store.ErrChargeNotFound
	}
	cp := *c
	return &cp, nil
}

func (f *fakeCharges) ListCharges(_ context.Context, merchantID, mode string, filter store.ChargeFilter) ([]*models.Charge, string, error) {
	if filter.Cursor == "bad" {
		return nil, "", store.ErrInvalidCursor
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*models.Charge
	for _, c := range f.byID {
		if c.MerchantID == merchantID && c.Mode == mode {
			if filter.Status != "" && c.Status != filter.Status {
				continue
			}
			cp := *c
			out = append(out, &cp)
		}
	}
	return out, "", nil
}

func (f *fakeCharges) SetRefund(_ context.Context, id, merchantID string, refundedAmount int64, status string) (*models.Charge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.byID[id]
	if !ok || c.MerchantID != merchantID {
		return nil, store.ErrChargeNotFound
	}
	c.RefundedAmount, c.Status, c.UpdatedAt = refundedAmount, status, time.Now()
	cp := *c
	return &cp, nil
}

func (f *fakeCharges) UpdateStatusByProcessorID(_ context.Context, processorChargeID, status, failureCode, failureMessage string) (*models.Charge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.byID {
		if c.ProcessorChargeID == processorChargeID {
			c.Status, c.FailureCode, c.FailureMessage = status, failureCode, failureMessage
			cp := *c
			return &cp, nil
		}
	}
	return nil, store.ErrChargeNotFound
}

// fakeAudit records audit entries in memory.
type fakeAudit struct {
	mu      sync.Mutex
	entries []store.AuditEntry
	failErr error
}

func (f *fakeAudit) WriteAuditLog(_ context.Context, e store.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failErr != nil {
		return f.failErr
	}
	f.entries = append(f.entries, e)
	return nil
}

func (f *fakeAudit) actions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.entries {
		out = append(out, e.Action)
	}
	return out
}

// pad12 renders n as a zero-padded 12-digit string so the fakes produce
// well-formed UUID strings.
func pad12(n int) string {
	const digits = "000000000000"
	s := digits + itoa(n)
	return s[len(s)-12:]
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
