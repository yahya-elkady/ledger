package payment_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/yahya-elkady/ledger/internal/ledger"
	"github.com/yahya-elkady/ledger/internal/payment"
)

// --- in-memory fakes -------------------------------------------------------
//
// The service depends on interfaces, so its ledger-effect and idempotency
// behaviour can be proven without Postgres. The real DB path is exercised by
// the smoke demo. These fakes are deliberately strict: fakeRepo returns copies
// from its getters so a caller mutating a loaded payment cannot silently change
// the stored row — the same contract the real store has (rows are fresh).

type fakeRepo struct {
	mu       sync.Mutex
	byID     map[string]payment.Payment
	byKey    map[string]string // idempotency key -> id
	creates  int
	statusUp int
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		byID:  map[string]payment.Payment{},
		byKey: map[string]string{},
	}
}

func (r *fakeRepo) CreatePayment(_ context.Context, p *payment.Payment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byKey[p.IdempotencyKey]; ok {
		return errors.New("duplicate idempotency key") // DB UNIQUE backstop
	}
	r.byID[p.ID] = *p
	r.byKey[p.IdempotencyKey] = p.ID
	r.creates++
	return nil
}

func (r *fakeRepo) GetPayment(_ context.Context, id string) (*payment.Payment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.byID[id]
	if !ok {
		return nil, payment.ErrPaymentNotFound
	}
	cp := p // copy: callers must not mutate stored state directly
	return &cp, nil
}

func (r *fakeRepo) GetPaymentByIdempotencyKey(_ context.Context, key string) (*payment.Payment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byKey[key]
	if !ok {
		return nil, payment.ErrPaymentNotFound
	}
	p := r.byID[id]
	cp := p
	return &cp, nil
}

func (r *fakeRepo) UpdatePaymentStatus(_ context.Context, p *payment.Payment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[p.ID]; !ok {
		return payment.ErrPaymentNotFound
	}
	r.byID[p.ID] = *p
	r.statusUp++
	return nil
}

// count reports how many payment rows are stored.
func (r *fakeRepo) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byID)
}

// fakeLedger records EntrySets by transaction ID so tests can assert exactly
// which entries a transition produced.
type fakeLedger struct {
	mu      sync.Mutex
	byTxID  map[string]ledger.EntrySet
	storeFn func() error // optional injected failure
}

func newFakeLedger() *fakeLedger {
	return &fakeLedger{byTxID: map[string]ledger.EntrySet{}}
}

func (l *fakeLedger) Store(_ context.Context, txID string, es ledger.EntrySet) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.storeFn != nil {
		if err := l.storeFn(); err != nil {
			return err
		}
	}
	l.byTxID[txID] = es
	return nil
}

func (l *fakeLedger) get(txID string) (ledger.EntrySet, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	es, ok := l.byTxID[txID]
	return es, ok
}

func (l *fakeLedger) txCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.byTxID)
}

// --- helpers ---------------------------------------------------------------

type harness struct {
	svc      *payment.Service
	repo     *fakeRepo
	ledger   *fakeLedger
	provider *payment.FakeProvider
}

func newHarness() *harness {
	repo := newFakeRepo()
	led := newFakeLedger()
	prov := &payment.FakeProvider{AuthorizeRef: "ref_test"}
	return &harness{
		svc:      payment.NewService(repo, led, prov),
		repo:     repo,
		ledger:   led,
		provider: prov,
	}
}

const usd = "USD"

func money(t *testing.T, n int64) ledger.Money {
	t.Helper()
	return ledger.MustMoney(n, usd)
}

// assertLeg checks that an EntrySet's single debit (or credit) hits the
// expected account for the expected amount.
func assertLeg(t *testing.T, es ledger.EntrySet, dir ledger.Direction, account string, amount int64) {
	t.Helper()
	for _, e := range es.Entries() {
		if e.Direction != dir {
			continue
		}
		if e.AccountID != account {
			t.Errorf("%s leg account = %q, want %q", dir, e.AccountID, account)
		}
		if e.Amount.Amount != amount {
			t.Errorf("%s leg amount = %d, want %d", dir, e.Amount.Amount, amount)
		}
		return
	}
	t.Errorf("no %s entry found in set", dir)
}

// createAndAuthorize drives a payment to authorized, failing the test on error.
func (h *harness) createAndAuthorize(t *testing.T, id, key string, amount int64) *payment.Payment {
	t.Helper()
	if _, err := h.svc.CreatePayment(context.Background(), id, money(t, amount), key); err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	p, err := h.svc.Authorize(context.Background(), id)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	return p
}

// --- tests -----------------------------------------------------------------

func TestAuthorizeLedgerEffects(t *testing.T) {
	h := newHarness()
	p := h.createAndAuthorize(t, "pay_auth", "key_auth", 1099)

	if p.Status != payment.StatusAuthorized {
		t.Fatalf("status = %q, want authorized", p.Status)
	}
	if p.ProviderRef != "ref_test" {
		t.Errorf("ProviderRef = %q, want ref_test", p.ProviderRef)
	}
	if h.provider.AuthorizeCalls != 1 {
		t.Errorf("AuthorizeCalls = %d, want 1", h.provider.AuthorizeCalls)
	}

	es, ok := h.ledger.get("pay_auth:authorize")
	if !ok {
		t.Fatal("no entry set written under pay_auth:authorize")
	}
	// Hold: debit cash-in-transit (asset up), credit authorization-hold (liability up).
	assertLeg(t, es, ledger.Debit, payment.AccountCashInTransit, 1099)
	assertLeg(t, es, ledger.Credit, payment.AccountAuthorizationHold, 1099)

	if h.ledger.txCount() != 1 {
		t.Errorf("ledger tx count = %d, want 1", h.ledger.txCount())
	}
}

func TestCaptureLedgerEffects(t *testing.T) {
	h := newHarness()
	h.createAndAuthorize(t, "pay_cap", "key_cap", 500)

	p, err := h.svc.Capture(context.Background(), "pay_cap")
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if p.Status != payment.StatusCaptured {
		t.Fatalf("status = %q, want captured", p.Status)
	}
	if h.provider.CaptureCalls != 1 {
		t.Errorf("CaptureCalls = %d, want 1", h.provider.CaptureCalls)
	}

	es, ok := h.ledger.get("pay_cap:capture")
	if !ok {
		t.Fatal("no entry set written under pay_cap:capture")
	}
	// Settle: debit authorization-hold (clears hold), credit settled-funds.
	assertLeg(t, es, ledger.Debit, payment.AccountAuthorizationHold, 500)
	assertLeg(t, es, ledger.Credit, payment.AccountSettledFunds, 500)

	// Two transactions total: the authorize hold and the capture settlement.
	if h.ledger.txCount() != 2 {
		t.Errorf("ledger tx count = %d, want 2", h.ledger.txCount())
	}
}

func TestVoidLedgerEffects(t *testing.T) {
	h := newHarness()
	h.createAndAuthorize(t, "pay_void", "key_void", 750)

	p, err := h.svc.Void(context.Background(), "pay_void")
	if err != nil {
		t.Fatalf("Void: %v", err)
	}
	if p.Status != payment.StatusVoided {
		t.Fatalf("status = %q, want voided", p.Status)
	}
	if h.provider.VoidCalls != 1 {
		t.Errorf("VoidCalls = %d, want 1", h.provider.VoidCalls)
	}

	es, ok := h.ledger.get("pay_void:void")
	if !ok {
		t.Fatal("no entry set written under pay_void:void")
	}
	// Release: debit authorization-hold (clears hold), credit cash-in-transit
	// (mirrors the authorize debit → net zero for this payment).
	assertLeg(t, es, ledger.Debit, payment.AccountAuthorizationHold, 750)
	assertLeg(t, es, ledger.Credit, payment.AccountCashInTransit, 750)
}

func TestIllegalTransitionsWriteNoEntries(t *testing.T) {
	t.Run("capture from pending", func(t *testing.T) {
		h := newHarness()
		if _, err := h.svc.CreatePayment(context.Background(), "p", money(t, 100), "k"); err != nil {
			t.Fatalf("CreatePayment: %v", err)
		}
		_, err := h.svc.Capture(context.Background(), "p")
		if !errors.Is(err, payment.ErrInvalidTransition) {
			t.Errorf("got %v, want ErrInvalidTransition", err)
		}
		if h.ledger.txCount() != 0 {
			t.Errorf("ledger tx count = %d, want 0", h.ledger.txCount())
		}
		if h.provider.CaptureCalls != 0 {
			t.Errorf("provider Capture was called %d times, want 0", h.provider.CaptureCalls)
		}
	})

	t.Run("void after capture", func(t *testing.T) {
		h := newHarness()
		h.createAndAuthorize(t, "p", "k", 100)
		if _, err := h.svc.Capture(context.Background(), "p"); err != nil {
			t.Fatalf("Capture: %v", err)
		}
		before := h.ledger.txCount()
		_, err := h.svc.Void(context.Background(), "p")
		if !errors.Is(err, payment.ErrInvalidTransition) {
			t.Errorf("got %v, want ErrInvalidTransition", err)
		}
		if h.ledger.txCount() != before {
			t.Errorf("void wrote entries after capture: tx count %d -> %d", before, h.ledger.txCount())
		}
	})

	t.Run("authorize twice", func(t *testing.T) {
		h := newHarness()
		h.createAndAuthorize(t, "p", "k", 100)
		_, err := h.svc.Authorize(context.Background(), "p")
		if !errors.Is(err, payment.ErrInvalidTransition) {
			t.Errorf("got %v, want ErrInvalidTransition", err)
		}
		// Only the first authorize wrote a hold; the provider isn't called again.
		if h.ledger.txCount() != 1 {
			t.Errorf("ledger tx count = %d, want 1", h.ledger.txCount())
		}
		if h.provider.AuthorizeCalls != 1 {
			t.Errorf("AuthorizeCalls = %d, want 1", h.provider.AuthorizeCalls)
		}
	})
}

func TestAuthorizeDecline(t *testing.T) {
	h := newHarness()
	h.provider.DeclineAuthorize = true

	if _, err := h.svc.CreatePayment(context.Background(), "p", money(t, 100), "k"); err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	p, err := h.svc.Authorize(context.Background(), "p")
	if err != nil {
		t.Fatalf("Authorize returned error on decline, want nil: %v", err)
	}
	if p.Status != payment.StatusFailed {
		t.Errorf("status = %q, want failed", p.Status)
	}
	if h.ledger.txCount() != 0 {
		t.Errorf("decline wrote %d ledger transactions, want 0", h.ledger.txCount())
	}

	// The failed status was persisted.
	reloaded, err := h.repo.GetPayment(context.Background(), "p")
	if err != nil {
		t.Fatalf("GetPayment: %v", err)
	}
	if reloaded.Status != payment.StatusFailed {
		t.Errorf("persisted status = %q, want failed", reloaded.Status)
	}
}

func TestProviderErrorOnAuthorizeLeavesPending(t *testing.T) {
	h := newHarness()
	h.provider.AuthorizeErr = errors.New("processor unreachable")

	if _, err := h.svc.CreatePayment(context.Background(), "p", money(t, 100), "k"); err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	_, err := h.svc.Authorize(context.Background(), "p")
	if err == nil {
		t.Fatal("Authorize returned nil, want provider error")
	}
	if h.ledger.txCount() != 0 {
		t.Errorf("provider error wrote %d ledger transactions, want 0", h.ledger.txCount())
	}

	reloaded, _ := h.repo.GetPayment(context.Background(), "p")
	if reloaded.Status != payment.StatusPending {
		t.Errorf("persisted status = %q, want pending (retryable)", reloaded.Status)
	}
}

func TestProviderErrorOnCaptureLeavesAuthorized(t *testing.T) {
	h := newHarness()
	h.createAndAuthorize(t, "p", "k", 100)
	h.provider.CaptureErr = errors.New("processor unreachable")

	before := h.ledger.txCount()
	_, err := h.svc.Capture(context.Background(), "p")
	if err == nil {
		t.Fatal("Capture returned nil, want provider error")
	}
	if h.ledger.txCount() != before {
		t.Errorf("capture error wrote entries: tx count %d -> %d", before, h.ledger.txCount())
	}

	// State left consistent: still authorized, so the caller can retry.
	reloaded, _ := h.repo.GetPayment(context.Background(), "p")
	if reloaded.Status != payment.StatusAuthorized {
		t.Errorf("persisted status = %q, want authorized after capture error", reloaded.Status)
	}
}

func TestIdempotency(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	amount := money(t, 1099)

	p1, err := h.svc.CreatePayment(ctx, "pay_idem", amount, "key_shared")
	if err != nil {
		t.Fatalf("first CreatePayment: %v", err)
	}
	if _, err := h.svc.Authorize(ctx, p1.ID); err != nil {
		t.Fatalf("Authorize: %v", err)
	}

	// Same key again — even with a different proposed ID — returns the existing
	// payment and creates nothing new.
	p2, err := h.svc.CreatePayment(ctx, "pay_different_id", amount, "key_shared")
	if err != nil {
		t.Fatalf("second CreatePayment: %v", err)
	}

	if p2.ID != p1.ID {
		t.Errorf("second create returned id %q, want existing %q", p2.ID, p1.ID)
	}
	if p2.Status != payment.StatusAuthorized {
		t.Errorf("returned payment status = %q, want the existing authorized payment", p2.Status)
	}
	if h.repo.count() != 1 {
		t.Errorf("payment rows = %d, want 1", h.repo.count())
	}
	if h.repo.creates != 1 {
		t.Errorf("CreatePayment persisted %d rows, want 1", h.repo.creates)
	}
	// Exactly one set of ledger entries: the single authorize hold.
	if h.ledger.txCount() != 1 {
		t.Errorf("ledger tx count = %d, want 1", h.ledger.txCount())
	}
}

func TestCreatePaymentValidation(t *testing.T) {
	h := newHarness()
	_, err := h.svc.CreatePayment(context.Background(), "", money(t, 100), "k")
	if !errors.Is(err, payment.ErrInvalidPayment) {
		t.Errorf("got %v, want ErrInvalidPayment", err)
	}
	if h.repo.count() != 0 {
		t.Errorf("invalid payment was persisted: count = %d", h.repo.count())
	}
}

func TestAuthorizeUnknownPayment(t *testing.T) {
	h := newHarness()
	_, err := h.svc.Authorize(context.Background(), "nope")
	if !errors.Is(err, payment.ErrPaymentNotFound) {
		t.Errorf("got %v, want ErrPaymentNotFound", err)
	}
}
