package payment

import (
	"context"

	"github.com/yahya-elkady/ledger/internal/ledger"
)

// FakeProvider is a fully controllable Provider for tests.
//
// By default every call succeeds: Authorize returns AuthorizeRef and Capture
// and Void return nil. Tests flip the configuration fields to exercise failure
// paths deterministically:
//
//   - DeclineAuthorize makes Authorize return ErrDeclined (a refused card).
//   - AuthorizeErr/CaptureErr/VoidErr inject a transport/processor error on the
//     matching method.
//
// The *Calls counters let tests assert which methods ran and how often.
type FakeProvider struct {
	// AuthorizeRef is the providerRef returned by a successful Authorize.
	// Defaults to "fake-provider-ref" when left empty.
	AuthorizeRef string

	// DeclineAuthorize, when true, makes Authorize return ErrDeclined.
	DeclineAuthorize bool

	// AuthorizeErr, CaptureErr, and VoidErr, when non-nil, are returned by the
	// matching method to simulate an unexpected provider/transport error.
	// AuthorizeErr takes precedence over DeclineAuthorize.
	AuthorizeErr error
	CaptureErr   error
	VoidErr      error

	// Call counters for test assertions.
	AuthorizeCalls int
	CaptureCalls   int
	VoidCalls      int
}

// defaultProviderRef is handed back by Authorize when AuthorizeRef is unset,
// so a zero-value FakeProvider still yields a usable reference.
const defaultProviderRef = "fake-provider-ref"

var _ Provider = (*FakeProvider)(nil)

// Authorize records the call, then honours (in order) an injected error, a
// configured decline, or success returning the configured reference.
func (f *FakeProvider) Authorize(ctx context.Context, amount ledger.Money) (string, error) {
	f.AuthorizeCalls++
	if f.AuthorizeErr != nil {
		return "", f.AuthorizeErr
	}
	if f.DeclineAuthorize {
		return "", ErrDeclined
	}
	ref := f.AuthorizeRef
	if ref == "" {
		ref = defaultProviderRef
	}
	return ref, nil
}

// Capture records the call and returns CaptureErr if configured, else nil.
func (f *FakeProvider) Capture(ctx context.Context, providerRef string) error {
	f.CaptureCalls++
	return f.CaptureErr
}

// Void records the call and returns VoidErr if configured, else nil.
func (f *FakeProvider) Void(ctx context.Context, providerRef string) error {
	f.VoidCalls++
	return f.VoidErr
}
