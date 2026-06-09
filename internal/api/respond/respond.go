// Package respond centralizes HTTP JSON responses and the canonical error
// envelope so every handler and middleware returns the exact same shape
// (build.md "Error Response Format"):
//
//	{"error": "code", "message": "...", "param": "field?", "request_id": "..."}
//
// It is a leaf package (no imports of api/middleware or handlers) so both the
// middleware and the router can depend on it without an import cycle.
package respond

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog/log"
)

// Canonical error codes paired with their HTTP statuses (build.md).
const (
	CodeAuthenticationRequired = "authentication_required" // 401
	CodeInvalidAPIKey          = "invalid_api_key"          // 401
	CodeTokenExpired           = "token_expired"            // 401
	CodeInsufficientScope      = "insufficient_scope"       // 403
	CodeNotFound               = "not_found"                // 404
	CodeMethodNotAllowed       = "method_not_allowed"       // 405
	CodeValidationError        = "validation_error"         // 400
	CodeIdempotencyKeyRequired = "idempotency_key_required" // 400
	CodeIdempotencyConflict    = "idempotency_conflict"     // 409
	CodeRateLimitExceeded      = "rate_limit_exceeded"      // 429
	CodeProcessorError         = "processor_error"          // 502
	CodeInternalError          = "internal_error"           // 500
)

// errorBody is the JSON error envelope returned on every error.
type errorBody struct {
	Error     string `json:"error"`
	Message   string `json:"message"`
	Param     string `json:"param,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// JSON writes v as a JSON response with the given status code.
func JSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Ctx(r.Context()).Error().Err(err).Msg("encoding json response")
	}
}

// Error writes the canonical error envelope with the given status and code.
func Error(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	ErrorParam(w, r, status, code, message, "")
}

// ErrorParam is Error with an offending field name (for validation errors).
func ErrorParam(w http.ResponseWriter, r *http.Request, status int, code, message, param string) {
	JSON(w, r, status, errorBody{
		Error:     code,
		Message:   message,
		Param:     param,
		RequestID: middleware.GetReqID(r.Context()),
	})
}
