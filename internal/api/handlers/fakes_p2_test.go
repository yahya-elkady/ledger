package handlers_test

import (
	"context"
	"sync"
	"time"

	"github.com/yahya-elkady/ledger/internal/models"
	"github.com/yahya-elkady/ledger/internal/store"
)

// fakePlans is an in-memory PlanStore.
type fakePlans struct {
	mu   sync.Mutex
	byID map[string]*models.Plan
	seq  int
}

func newFakePlans() *fakePlans { return &fakePlans{byID: map[string]*models.Plan{}} }

func (f *fakePlans) CreatePlan(_ context.Context, p store.NewPlan) (*models.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	m := &models.Plan{
		ID: "55555555-5555-5555-5555-" + pad12(f.seq), MerchantID: p.MerchantID, Name: p.Name,
		Amount: p.Amount, Currency: p.Currency, Interval: p.Interval, IntervalCount: p.IntervalCount,
		ProcessorPlanID: p.ProcessorPlanID, Mode: p.Mode, CreatedAt: time.Now(),
	}
	f.byID[m.ID] = m
	return m, nil
}

func (f *fakePlans) ListPlans(_ context.Context, merchantID, mode string) ([]*models.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*models.Plan
	for _, p := range f.byID {
		if p.MerchantID == merchantID && p.Mode == mode {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakePlans) GetPlan(_ context.Context, id, merchantID, mode string) (*models.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.byID[id]
	if !ok || p.MerchantID != merchantID || p.Mode != mode {
		return nil, store.ErrPlanNotFound
	}
	return p, nil
}

func (f *fakePlans) SoftDeletePlan(_ context.Context, id, merchantID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.byID[id]
	if !ok || p.MerchantID != merchantID {
		return store.ErrPlanNotFound
	}
	delete(f.byID, id)
	return nil
}

// fakeSubscriptions is an in-memory SubscriptionStore.
type fakeSubscriptions struct {
	mu   sync.Mutex
	byID map[string]*models.Subscription
	seq  int
}

func newFakeSubscriptions() *fakeSubscriptions {
	return &fakeSubscriptions{byID: map[string]*models.Subscription{}}
}

func (f *fakeSubscriptions) CreateSubscription(_ context.Context, s store.NewSubscription) (*models.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	m := &models.Subscription{
		ID: "66666666-6666-6666-6666-" + pad12(f.seq), MerchantID: s.MerchantID, CustomerID: s.CustomerID,
		PlanID: s.PlanID, PaymentMethodID: s.PaymentMethodID, Status: s.Status, ProcessorSubID: s.ProcessorSubID,
		Mode: s.Mode, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	f.byID[m.ID] = m
	return m, nil
}

func (f *fakeSubscriptions) GetSubscription(_ context.Context, id, merchantID, mode string) (*models.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.byID[id]
	if !ok || s.MerchantID != merchantID || s.Mode != mode {
		return nil, store.ErrSubscriptionNotFound
	}
	return s, nil
}

func (f *fakeSubscriptions) ListSubscriptions(_ context.Context, merchantID, mode string, _ store.SubscriptionFilter) ([]*models.Subscription, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*models.Subscription
	for _, s := range f.byID {
		if s.MerchantID == merchantID && s.Mode == mode {
			out = append(out, s)
		}
	}
	return out, "", nil
}

func (f *fakeSubscriptions) SetSubscriptionStatus(_ context.Context, id, merchantID, status string, _ bool) (*models.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.byID[id]
	if !ok || s.MerchantID != merchantID {
		return nil, store.ErrSubscriptionNotFound
	}
	s.Status = status
	return s, nil
}

func (f *fakeSubscriptions) UpdateStatusByProcessorID(_ context.Context, processorSubID, status string) (*models.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.byID {
		if s.ProcessorSubID == processorSubID {
			s.Status = status
			return s, nil
		}
	}
	return nil, store.ErrSubscriptionNotFound
}

// fakeBankAccounts is an in-memory BankAccountStore.
type fakeBankAccounts struct {
	mu   sync.Mutex
	byID map[string]*models.BankAccount
	seq  int
}

func newFakeBankAccounts() *fakeBankAccounts {
	return &fakeBankAccounts{byID: map[string]*models.BankAccount{}}
}

func (f *fakeBankAccounts) CreateBankAccount(_ context.Context, b store.NewBankAccount) (*models.BankAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	m := &models.BankAccount{
		ID: "77777777-7777-7777-7777-" + pad12(f.seq), MerchantID: b.MerchantID, Processor: b.Processor,
		ProcessorAcctID: b.ProcessorAcctID, Last4: b.Last4, BankName: b.BankName, Currency: b.Currency,
		IsDefault: b.IsDefault, CreatedAt: time.Now(),
	}
	f.byID[m.ID] = m
	return m, nil
}

func (f *fakeBankAccounts) ListBankAccounts(_ context.Context, merchantID string) ([]*models.BankAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*models.BankAccount
	for _, b := range f.byID {
		if b.MerchantID == merchantID {
			out = append(out, b)
		}
	}
	return out, nil
}

func (f *fakeBankAccounts) SoftDeleteBankAccount(_ context.Context, id, merchantID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.byID[id]
	if !ok || b.MerchantID != merchantID {
		return store.ErrBankAccountNotFound
	}
	delete(f.byID, id)
	return nil
}

// fakePayouts is an in-memory PayoutStore.
type fakePayouts struct {
	mu   sync.Mutex
	byID map[string]*models.Payout
	seq  int
}

func newFakePayouts() *fakePayouts { return &fakePayouts{byID: map[string]*models.Payout{}} }

func (f *fakePayouts) CreatePayout(_ context.Context, p store.NewPayout) (*models.Payout, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	m := &models.Payout{
		ID: "88888888-8888-8888-8888-" + pad12(f.seq), MerchantID: p.MerchantID, BankAccountID: p.BankAccountID,
		Amount: p.Amount, Currency: p.Currency, Status: p.Status, Processor: p.Processor,
		ProcessorPayoutID: p.ProcessorPayoutID, Mode: p.Mode, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	f.byID[m.ID] = m
	return m, nil
}

func (f *fakePayouts) GetPayout(_ context.Context, id, merchantID, mode string) (*models.Payout, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.byID[id]
	if !ok || p.MerchantID != merchantID || p.Mode != mode {
		return nil, store.ErrPayoutNotFound
	}
	return p, nil
}

func (f *fakePayouts) ListPayouts(_ context.Context, merchantID, mode string, _ int, _ string) ([]*models.Payout, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*models.Payout
	for _, p := range f.byID {
		if p.MerchantID == merchantID && p.Mode == mode {
			out = append(out, p)
		}
	}
	return out, "", nil
}

func (f *fakePayouts) UpdateStatusByProcessorID(_ context.Context, processorPayoutID, status, failureMessage string) (*models.Payout, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.byID {
		if p.ProcessorPayoutID == processorPayoutID {
			p.Status, p.FailureMessage = status, failureMessage
			return p, nil
		}
	}
	return nil, store.ErrPayoutNotFound
}

// fakeDashboard is an in-memory DashboardStore returning canned aggregates.
type fakeDashboard struct {
	stats          store.ChargeStats
	activeSubs     int64
	pendingPayouts int64
	failed         []*models.Charge
}

func (f *fakeDashboard) ChargeStats(_ context.Context, _, _ string) (store.ChargeStats, error) {
	return f.stats, nil
}
func (f *fakeDashboard) CountActiveSubscriptions(_ context.Context, _, _ string) (int64, error) {
	return f.activeSubs, nil
}
func (f *fakeDashboard) CountPendingPayouts(_ context.Context, _, _ string) (int64, error) {
	return f.pendingPayouts, nil
}
func (f *fakeDashboard) RecentFailedCharges(_ context.Context, _, _ string, _ int) ([]*models.Charge, error) {
	return f.failed, nil
}
