package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yahya-elkady/ledger/internal/metrics"
)

// TestHandlerExposesRecordedMetrics records one of each metric and asserts the
// scrape output contains the expected collector names. The collectors live on a
// process-global registry, so this also exercises the real Handler path.
func TestHandlerExposesRecordedMetrics(t *testing.T) {
	metrics.HTTPRequest(http.MethodPost, "/v1/charges", 201, 12*time.Millisecond)
	metrics.Charge("succeeded", "USD", "stripe", "test", 1999)
	metrics.RateLimitHit("api_key", "live")
	metrics.WebhookDelivery("delivered")

	rr := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want 200", rr.Code)
	}

	body := rr.Body.String()
	for _, name := range []string{
		"http_requests_total",
		"http_request_duration_seconds",
		"payment_charges_total",
		"payment_charges_amount_total",
		"rate_limit_hits_total",
		"webhook_deliveries_total",
	} {
		if !strings.Contains(body, name) {
			t.Errorf("scrape output missing metric %q", name)
		}
	}
}
