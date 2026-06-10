// Package metrics defines the Prometheus collectors the service exposes at
// /metrics and the thin helpers handlers/middleware call to record them.
//
// It is a leaf package (imports only the Prometheus client) so any layer can
// record without creating an import cycle. Collectors register on the default
// registry via promauto; Handler serves that registry.
//
// Cardinality note: the HTTP path label is the chi route *pattern*
// (e.g. /v1/charges/{id}), never the raw URL, so per-id paths do not explode
// the label space.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	httpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total HTTP requests handled, by method, route pattern, and status.",
	}, []string{"method", "path", "status"})

	httpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request latency in seconds, by method and route pattern.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})

	paymentChargesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "payment_charges_total",
		Help: "Total charges created, by status, currency, processor, and mode.",
	}, []string{"status", "currency", "processor", "mode"})

	paymentChargesAmountTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "payment_charges_amount_total",
		Help: "Total charge amount in minor units (cents), by status, currency, processor, and mode.",
	}, []string{"status", "currency", "processor", "mode"})

	rateLimitHitsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "rate_limit_hits_total",
		Help: "Requests rejected by the rate limiter, by client type and mode.",
	}, []string{"client_type", "mode"})

	webhookDeliveriesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "webhook_deliveries_total",
		Help: "Terminal outbound webhook delivery outcomes, by status.",
	}, []string{"status"})
)

// Handler returns the Prometheus scrape handler for the default registry.
func Handler() http.Handler { return promhttp.Handler() }

// HTTPRequest records one served request's count and latency. path should be the
// chi route pattern to keep label cardinality bounded.
func HTTPRequest(method, path string, status int, dur time.Duration) {
	httpRequestsTotal.WithLabelValues(method, path, strconv.Itoa(status)).Inc()
	httpRequestDuration.WithLabelValues(method, path).Observe(dur.Seconds())
}

// Charge records a created charge's count and amount (minor units).
func Charge(status, currency, processor, mode string, amount int64) {
	labels := []string{status, currency, processor, mode}
	paymentChargesTotal.WithLabelValues(labels...).Inc()
	paymentChargesAmountTotal.WithLabelValues(labels...).Add(float64(amount))
}

// RateLimitHit records a request rejected by the rate limiter.
func RateLimitHit(clientType, mode string) {
	rateLimitHitsTotal.WithLabelValues(clientType, mode).Inc()
}

// WebhookDelivery records a terminal outbound delivery outcome ("delivered" or
// "failed"); pending retries are not terminal and are not counted here.
func WebhookDelivery(status string) {
	webhookDeliveriesTotal.WithLabelValues(status).Inc()
}
