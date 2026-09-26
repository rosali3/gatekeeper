package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// Each test uses a unique label value so counts from other tests (these
// are package-level, globally-registered metrics) can't bleed in.

func TestRequestsTotal_Increments(t *testing.T) {
	RequestsTotal.WithLabelValues("/metrics-test-a/", "200").Inc()
	if got := testutil.ToFloat64(RequestsTotal.WithLabelValues("/metrics-test-a/", "200")); got != 1 {
		t.Errorf("RequestsTotal = %v, want 1", got)
	}
}

func TestRequestDuration_Observes(t *testing.T) {
	RequestDuration.WithLabelValues("/metrics-test-b/").Observe(0.05)
	if count := testutil.CollectAndCount(RequestDuration); count == 0 {
		t.Error("RequestDuration: expected at least one observation to be collectible")
	}
}

func TestUpstreamDuration_Observes(t *testing.T) {
	UpstreamDuration.WithLabelValues("demo-upstream", "http://backend-1").Observe(0.01)
	if count := testutil.CollectAndCount(UpstreamDuration); count == 0 {
		t.Error("UpstreamDuration: expected at least one observation to be collectible")
	}
}

func TestRateLimitRejected_Increments(t *testing.T) {
	RateLimitRejected.WithLabelValues("/metrics-test-c/").Inc()
	if got := testutil.ToFloat64(RateLimitRejected.WithLabelValues("/metrics-test-c/")); got != 1 {
		t.Errorf("RateLimitRejected = %v, want 1", got)
	}
}

func TestCircuitState_Sets(t *testing.T) {
	CircuitState.WithLabelValues("metrics-test-upstream-d").Set(1)
	if got := testutil.ToFloat64(CircuitState.WithLabelValues("metrics-test-upstream-d")); got != 1 {
		t.Errorf("CircuitState = %v, want 1", got)
	}
	CircuitState.WithLabelValues("metrics-test-upstream-d").Set(0)
	if got := testutil.ToFloat64(CircuitState.WithLabelValues("metrics-test-upstream-d")); got != 0 {
		t.Errorf("CircuitState = %v, want 0", got)
	}
}

func TestUpstreamHealthy_Sets(t *testing.T) {
	UpstreamHealthy.WithLabelValues("metrics-test-upstream-e", "http://backend-1").Set(1)
	if got := testutil.ToFloat64(UpstreamHealthy.WithLabelValues("metrics-test-upstream-e", "http://backend-1")); got != 1 {
		t.Errorf("UpstreamHealthy = %v, want 1", got)
	}
}

func TestRetriesTotal_Increments(t *testing.T) {
	RetriesTotal.WithLabelValues("metrics-test-upstream-f").Inc()
	if got := testutil.ToFloat64(RetriesTotal.WithLabelValues("metrics-test-upstream-f")); got != 1 {
		t.Errorf("RetriesTotal = %v, want 1", got)
	}
}

func TestActiveConnections_IncDec(t *testing.T) {
	g := ActiveConnections.WithLabelValues("metrics-test-upstream-g")
	g.Inc()
	g.Inc()
	if got := testutil.ToFloat64(g); got != 2 {
		t.Errorf("ActiveConnections = %v, want 2", got)
	}
	g.Dec()
	if got := testutil.ToFloat64(g); got != 1 {
		t.Errorf("ActiveConnections = %v, want 1", got)
	}
}
