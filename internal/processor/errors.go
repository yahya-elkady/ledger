package processor

import "fmt"

// ErrorCode is a processor-agnostic classification of a failed processor call,
// so callers and logs see a consistent shape regardless of vendor (build.md:
// "processor errors mapped to internal error codes with consistent structure").
type ErrorCode string

const (
	// CodeRateLimited — the processor throttled us (HTTP 429). Transient.
	CodeRateLimited ErrorCode = "rate_limited"
	// CodeUnavailable — the processor errored or was unreachable (5xx/network).
	// Transient.
	CodeUnavailable ErrorCode = "processor_unavailable"
	// CodeInvalidRequest — the processor rejected the request as malformed.
	// Not retryable.
	CodeInvalidRequest ErrorCode = "invalid_request"
	// CodeAuth — credentials were rejected. Not retryable.
	CodeAuth ErrorCode = "authentication_error"
	// CodeUnknown — an unclassified failure. Not retryable by default.
	CodeUnknown ErrorCode = "unknown"
)

// Error is the normalized error returned by a processor adapter. Card declines
// are NOT represented here — they are a successful call with a "failed" result
// (ChargeResult.Status == "failed"); Error is for operational failures.
type Error struct {
	Code      ErrorCode
	Message   string
	Retryable bool
	cause     error
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("processor %s: %s: %v", e.Code, e.Message, e.cause)
	}
	return fmt.Sprintf("processor %s: %s", e.Code, e.Message)
}

// Unwrap exposes the underlying vendor error for errors.Is/As.
func (e *Error) Unwrap() error { return e.cause }

// newError builds a normalized processor Error.
func newError(code ErrorCode, retryable bool, cause error, format string, args ...any) *Error {
	return &Error{Code: code, Retryable: retryable, cause: cause, Message: fmt.Sprintf(format, args...)}
}

// NewError is the exported constructor vendor adapters (internal/processor/stripe,
// .../plaid) use to return normalized processor errors.
func NewError(code ErrorCode, retryable bool, cause error, format string, args ...any) *Error {
	return newError(code, retryable, cause, format, args...)
}

// isRetryable reports whether an error should be retried — true for *Error
// values flagged Retryable. Vendor adapters wrap their transient errors in an
// *Error with Retryable=true.
func isRetryable(err error) bool {
	pe, ok := err.(*Error)
	return ok && pe.Retryable
}
