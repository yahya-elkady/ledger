package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeDeliveryStore is an in-memory DeliveryStore.
type fakeDeliveryStore struct {
	mu        sync.Mutex
	endpoints []Endpoint
	rows      map[string]*deliveryRow
	nextID    int
}

type deliveryRow struct {
	Delivery
	Status      string
	NextRetryAt *time.Time
	LastStatus  int
	LastBody    string
}

func newFakeStore(endpoints ...Endpoint) *fakeDeliveryStore {
	return &fakeDeliveryStore{endpoints: endpoints, rows: map[string]*deliveryRow{}}
}

func (f *fakeDeliveryStore) ActiveEndpoints(_ context.Context, _, _, _ string) ([]Endpoint, error) {
	return f.endpoints, nil
}

func (f *fakeDeliveryStore) CreateDelivery(_ context.Context, endpointID, eventType string, payload []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	id := "dl_" + strconv.Itoa(f.nextID)
	var url string
	for _, ep := range f.endpoints {
		if ep.ID == endpointID {
			url = ep.URL
		}
	}
	f.rows[id] = &deliveryRow{
		Delivery: Delivery{ID: id, EndpointID: endpointID, EventType: eventType, Payload: payload, URL: url},
		Status:   StatusPending,
	}
	return id, nil
}

func (f *fakeDeliveryStore) DueDeliveries(_ context.Context, limit int) ([]Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var due []Delivery
	now := time.Now()
	for _, row := range f.rows {
		if row.Status == StatusPending && (row.NextRetryAt == nil || !row.NextRetryAt.After(now)) {
			due = append(due, row.Delivery)
			if len(due) == limit {
				break
			}
		}
	}
	return due, nil
}

func (f *fakeDeliveryStore) FinishAttempt(_ context.Context, deliveryID string, o AttemptOutcome) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[deliveryID]
	if !ok {
		return fmt.Errorf("no such delivery %s", deliveryID)
	}
	row.AttemptCount++
	row.Status = o.Status
	row.NextRetryAt = o.NextRetryAt
	row.LastStatus = o.ResponseStatus
	row.LastBody = o.ResponseBody
	return nil
}

func (f *fakeDeliveryStore) row(t *testing.T, id string) deliveryRow {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		t.Fatalf("delivery %s not found", id)
	}
	return *row
}

const testMaster = "master_webhook_secret_32_chars_long!!"

func newTestDispatcher(store DeliveryStore) *Dispatcher {
	return NewDispatcher(store, DispatcherConfig{
		SigningSecret: testMaster,
		MaxAttempts:   3,
		BaseBackoff:   time.Minute,
	})
}

func TestDispatchCreatesPendingDeliveries(t *testing.T) {
	store := newFakeStore(Endpoint{ID: "ep_1", URL: "http://a"}, Endpoint{ID: "ep_2", URL: "http://b"})
	d := newTestDispatcher(store)

	if err := d.Dispatch(context.Background(), "m_1", "test", "charge.succeeded", map[string]string{"id": "ch_1"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(store.rows) != 2 {
		t.Errorf("got %d deliveries, want one per subscribed endpoint (2)", len(store.rows))
	}
	for id := range store.rows {
		row := store.row(t, id)
		if row.Status != StatusPending || row.EventType != "charge.succeeded" {
			t.Errorf("row %s = %s/%s, want pending/charge.succeeded", id, row.Status, row.EventType)
		}
	}
}

func TestDeliverSuccessSignsAndMarksDelivered(t *testing.T) {
	var gotSig, gotTS string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-Payments-Signature")
		gotTS = r.Header.Get("X-Payments-Timestamp")
		gotBody = make([]byte, r.ContentLength)
		_, _ = r.Body.Read(gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := newFakeStore(Endpoint{ID: "ep_1", URL: srv.URL})
	d := newTestDispatcher(store)
	if err := d.Dispatch(context.Background(), "m_1", "test", "charge.succeeded", map[string]string{"id": "ch_1"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if err := d.ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}

	row := store.row(t, "dl_1")
	if row.Status != StatusDelivered || row.AttemptCount != 1 || row.LastStatus != http.StatusOK {
		t.Errorf("after 2xx: status=%s attempts=%d last=%d, want delivered/1/200", row.Status, row.AttemptCount, row.LastStatus)
	}

	// The merchant must be able to verify the signature with the derived
	// per-endpoint secret and the timestamp header.
	ts, err := strconv.ParseInt(gotTS, 10, 64)
	if err != nil {
		t.Fatalf("bad timestamp header %q: %v", gotTS, err)
	}
	secret := DeriveEndpointSecret(testMaster, "ep_1")
	if gotSig != "sha256="+SignPayload(gotBody, secret, ts) {
		t.Error("signature header does not verify against the derived endpoint secret")
	}

	// Envelope shape: {"id","type","created","data"}.
	var envelope struct {
		ID      string          `json:"id"`
		Type    string          `json:"type"`
		Created int64           `json:"created"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(gotBody, &envelope); err != nil {
		t.Fatalf("unmarshaling envelope: %v", err)
	}
	if envelope.ID != "dl_1" || envelope.Type != "charge.succeeded" || envelope.Created == 0 {
		t.Errorf("unexpected envelope: %+v", envelope)
	}
}

func TestDeliverFailureSchedulesBackoffRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	store := newFakeStore(Endpoint{ID: "ep_1", URL: srv.URL})
	d := newTestDispatcher(store)
	_ = d.Dispatch(context.Background(), "m_1", "test", "charge.failed", map[string]string{"id": "ch_1"})

	before := time.Now()
	if err := d.ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}

	row := store.row(t, "dl_1")
	if row.Status != StatusPending || row.AttemptCount != 1 || row.LastStatus != http.StatusInternalServerError {
		t.Fatalf("after 5xx: status=%s attempts=%d last=%d, want pending/1/500", row.Status, row.AttemptCount, row.LastStatus)
	}
	if row.NextRetryAt == nil {
		t.Fatal("retry must be scheduled")
	}
	// First retry delay: base * 2^1 = 2m.
	wantMin := before.Add(2*time.Minute - time.Second)
	wantMax := before.Add(2*time.Minute + 10*time.Second)
	if row.NextRetryAt.Before(wantMin) || row.NextRetryAt.After(wantMax) {
		t.Errorf("next retry at %v, want ~2m after %v", row.NextRetryAt, before)
	}
}

func TestDeliverExhaustsAttemptsAndDeadLetters(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "still down", http.StatusBadGateway)
	}))
	defer srv.Close()

	store := newFakeStore(Endpoint{ID: "ep_1", URL: srv.URL})
	d := newTestDispatcher(store) // MaxAttempts: 3
	_ = d.Dispatch(context.Background(), "m_1", "test", "payout.failed", map[string]string{"id": "po_1"})

	for i := 0; i < 5; i++ { // more sweeps than attempts: extra sweeps must be no-ops
		store.mu.Lock()
		store.rows["dl_1"].NextRetryAt = nil // force due
		store.mu.Unlock()
		if store.row(t, "dl_1").Status != StatusPending {
			break
		}
		if err := d.ProcessDue(context.Background()); err != nil {
			t.Fatalf("ProcessDue: %v", err)
		}
	}

	row := store.row(t, "dl_1")
	if row.Status != StatusFailed {
		t.Errorf("status = %s, want failed (dead-lettered)", row.Status)
	}
	if row.AttemptCount != 3 || calls != 3 {
		t.Errorf("attempts = %d (http calls %d), want exactly MaxAttempts (3)", row.AttemptCount, calls)
	}
}

func TestDeliverTransportErrorRetries(t *testing.T) {
	// Unroutable URL: the HTTP call itself fails, with no response status.
	store := newFakeStore(Endpoint{ID: "ep_1", URL: "http://127.0.0.1:1"})
	d := newTestDispatcher(store)
	_ = d.Dispatch(context.Background(), "m_1", "test", "charge.succeeded", map[string]string{"id": "ch_1"})

	if err := d.ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	row := store.row(t, "dl_1")
	if row.Status != StatusPending || row.LastStatus != 0 {
		t.Errorf("transport error: status=%s last=%d, want pending/0", row.Status, row.LastStatus)
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	store := newFakeStore()
	d := NewDispatcher(store, DispatcherConfig{
		SigningSecret: testMaster,
		PollInterval:  10 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}
}
