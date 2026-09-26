// Package metrics defines gatekeeper's Prometheus metrics. Names and
// label sets follow the TZ's list; retries_total and active_connections
// additionally carry an {upstream} label the TZ's own prose omits - a
// gateway with more than one upstream gets far more value from either
// broken down per-upstream than from one process-wide number, and it
// costs nothing to add.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	RequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_requests_total",
		Help: "Total requests handled, by matched route and final HTTP status code.",
	}, []string{"route", "code"})

	RequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "gateway_request_duration_seconds",
		Help: "End-to-end request duration (whole middleware chain), by matched route.",
	}, []string{"route"})

	UpstreamDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "gateway_upstream_duration_seconds",
		Help: "Duration of a single proxied attempt to one target.",
	}, []string{"upstream", "target"})

	RateLimitRejected = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_ratelimit_rejected_total",
		Help: "Requests rejected with 429, by route.",
	}, []string{"route"})

	// CircuitState is 0=closed, 1=open, 2=half_open (breaker.State's own
	// ordering), so PromQL can alert on e.g. gateway_circuit_state > 0.
	CircuitState = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gateway_circuit_state",
		Help: "Circuit breaker state per upstream: 0=closed, 1=open, 2=half_open.",
	}, []string{"upstream"})

	UpstreamHealthy = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gateway_upstream_healthy",
		Help: "1 if the target is currently healthy, 0 otherwise.",
	}, []string{"upstream", "target"})

	RetriesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_retries_total",
		Help: "Total retry attempts issued, by upstream.",
	}, []string{"upstream"})

	ActiveConnections = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gateway_active_connections",
		Help: "Requests currently being proxied to an upstream.",
	}, []string{"upstream"})
)
