package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"

	"github.com/yahya-elkady/ledger/internal/api/respond"
)

// idempotencyTTL is how long a captured response is replayable. 24h matches the
// window a client might safely retry a create within.
const idempotencyTTL = 24 * time.Hour

// Idempotency provides replay-safe POST handling. The first request with a given
// key executes normally and its response is cached; subsequent requests with the
// same key replay that response without re-running the handler — so a retried
// "create charge" never charges twice.
type Idempotency struct {
	redis *redis.Client
}

// NewIdempotency wires the middleware to Redis.
func NewIdempotency(rdb *redis.Client) *Idempotency {
	return &Idempotency{redis: rdb}
}

// cachedResponse is the stored representation of a completed response. Body is
// JSON-encoded as base64 automatically for []byte.
type cachedResponse struct {
	Status      int    `json:"status"`
	Body        []byte `json:"body"`
	ContentType string `json:"content_type"`
}

// Handler enforces idempotency on POST requests. It must run after auth (it keys
// on the merchant) and is mounted only on authenticated write routes.
func (m *Idempotency) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only POST writes are idempotency-protected; everything else passes.
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}

		idemKey := r.Header.Get("Idempotency-Key")
		if idemKey == "" {
			respond.Error(w, r, http.StatusBadRequest, respond.CodeIdempotencyKeyRequired,
				"the Idempotency-Key header is required for this request")
			return
		}

		merchantID := MerchantID(r.Context())
		if merchantID == "" {
			// Idempotency is only mounted behind auth; a missing merchant means
			// a wiring bug, not a client error.
			log.Ctx(r.Context()).Error().Msg("idempotency middleware ran without an authenticated merchant")
			respond.Error(w, r, http.StatusInternalServerError, respond.CodeInternalError, "internal error")
			return
		}

		cacheKey := idempotencyCacheKey(merchantID, idemKey)

		// Replay a previously-captured response if present.
		if cached, ok := m.lookup(r.Context(), cacheKey); ok {
			// Idempotency keys are client-chosen request identifiers, not secrets.
			log.Ctx(r.Context()).Info().Str("merchant_id", merchantID).
				Str("idempotency_key", idemKey).Int("status", cached.Status).
				Msg("idempotent replay")
			if cached.ContentType != "" {
				w.Header().Set("Content-Type", cached.ContentType)
			}
			w.Header().Set("Idempotency-Replayed", "true")
			w.WriteHeader(cached.Status)
			_, _ = w.Write(cached.Body)
			return
		}

		// First time for this key: capture the response, then store and flush.
		rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		// Don't cache server errors — they're typically transient and the client
		// should be able to retry rather than replay a 500.
		if rec.status < http.StatusInternalServerError {
			m.store(r.Context(), cacheKey, cachedResponse{
				Status:      rec.status,
				Body:        rec.body.Bytes(),
				ContentType: w.Header().Get("Content-Type"),
			})
		}

		w.WriteHeader(rec.status)
		_, _ = w.Write(rec.body.Bytes())
	})
}

// lookup returns a cached response for the key, if any.
func (m *Idempotency) lookup(ctx context.Context, key string) (cachedResponse, bool) {
	raw, err := m.redis.Get(ctx, key).Bytes()
	if err != nil {
		return cachedResponse{}, false // miss or Redis down
	}
	var cr cachedResponse
	if json.Unmarshal(raw, &cr) != nil {
		return cachedResponse{}, false
	}
	return cr, true
}

// store caches a response under the key with NX semantics, so the first writer
// wins and a concurrent duplicate cannot clobber it. TTL is 24h.
func (m *Idempotency) store(ctx context.Context, key string, cr cachedResponse) {
	raw, err := json.Marshal(cr)
	if err != nil {
		return
	}
	if err := m.redis.SetNX(ctx, key, raw, idempotencyTTL).Err(); err != nil {
		log.Ctx(ctx).Warn().Err(err).Msg("failed to cache idempotent response")
	}
}

func idempotencyCacheKey(merchantID, idemKey string) string {
	return "idempotency:" + merchantID + ":" + idemKey
}

// responseRecorder buffers a handler's response so it can be both cached and
// then written to the real client. It captures status and body; headers are set
// directly on the embedded ResponseWriter by the handler, so they are already in
// place when we flush.
type responseRecorder struct {
	http.ResponseWriter
	status      int
	body        bytes.Buffer
	wroteHeader bool
}

// WriteHeader records the status without writing it through (the flush does that).
func (rec *responseRecorder) WriteHeader(code int) {
	if !rec.wroteHeader {
		rec.status = code
		rec.wroteHeader = true
	}
}

// Write buffers the body instead of writing through, so we can capture it.
func (rec *responseRecorder) Write(b []byte) (int, error) {
	if !rec.wroteHeader {
		rec.WriteHeader(http.StatusOK)
	}
	return rec.body.Write(b)
}
