package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"gatekeeper/internal/config"
	"gatekeeper/internal/metrics"
)

func TestGateway_RefreshMetrics_NoSnapshotIsNoop(t *testing.T) {
	gw := New()
	gw.RefreshMetrics() // must not panic
}

func TestGateway_RefreshMetrics_SetsHealthAndBreakerGauges(t *testing.T) {
	srv := backend(t, "demo-backend")
	cfg := baseConfig(srv.URL)

	gw := New()
	gw.Swap(buildSnapshot(t, cfg))
	gw.RefreshMetrics()

	target := gw.Current().Upstreams["demo"].Balancer.Targets()[0]
	if got := testutil.ToFloat64(metrics.UpstreamHealthy.WithLabelValues("demo", target.URL.String())); got != 1 {
		t.Errorf("UpstreamHealthy = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.CircuitState.WithLabelValues("demo")); got != 0 {
		t.Errorf("CircuitState = %v, want 0 (closed)", got)
	}
}

func TestGateway_RateLimitRejected_IncrementsMetric(t *testing.T) {
	srv := backend(t, "demo-backend")
	cfg := configWithRateLimit(srv.URL, config.RateLimitConfig{
		Key: config.RateLimitKeyIP, RPS: 1, Burst: 1, Backend: config.RateLimitBackendLocal,
	})

	gw := New()
	gw.Swap(buildSnapshot(t, cfg))

	req := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/demo/x", nil)
		r.RemoteAddr = "203.0.113.99:1"
		return r
	}
	gw.ServeHTTP(httptest.NewRecorder(), req()) // spends the only token
	gw.ServeHTTP(httptest.NewRecorder(), req()) // should be rejected

	if got := testutil.ToFloat64(metrics.RateLimitRejected.WithLabelValues("/api/demo/")); got == 0 {
		t.Error("expected gateway_ratelimit_rejected_total to have been incremented")
	}
}

func TestGateway_Retry_IncrementsRetriesTotal(t *testing.T) {
	bad, _ := statusBackend(t, http.StatusServiceUnavailable)
	good, _ := statusBackend(t, http.StatusOK)

	cfg := configWithRetries(bad.URL,
		[]config.Target{{URL: bad.URL, Weight: 1}, {URL: good.URL, Weight: 1}},
		config.RetriesConfig{Max: 1, OnlyIdempotent: true, BudgetRatio: 1.0},
		permissiveBreaker(),
	)

	gw := New()
	gw.Swap(buildSnapshot(t, cfg))

	before := testutil.ToFloat64(metrics.RetriesTotal.WithLabelValues("demo"))
	gw.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/demo/x", nil))
	after := testutil.ToFloat64(metrics.RetriesTotal.WithLabelValues("demo"))

	if after != before+1 {
		t.Errorf("RetriesTotal went from %v to %v, want +1", before, after)
	}
}

func TestGateway_TraceContext_PropagatedToUpstream(t *testing.T) {
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	otel.SetTextMapPropagator(propagation.TraceContext{})

	var sawTraceparent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawTraceparent = r.Header.Get("traceparent")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	cfg := baseConfig(srv.URL)
	gw := New()
	gw.Swap(buildSnapshot(t, cfg))

	gw.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/demo/x", nil))

	if sawTraceparent == "" {
		t.Error("upstream did not receive a traceparent header")
	}
}
