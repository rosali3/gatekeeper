package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"gatekeeper/internal/metrics"
	"gatekeeper/internal/reqctx"
)

func TestMetrics_RecordsRouteAndStatus(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulates route-match having run and populated the route.
		if fields := reqctx.AccessFieldsFrom(r.Context()); fields != nil {
			fields.Route = "/mw-metrics-test/"
		}
		w.WriteHeader(http.StatusCreated)
	})

	ctx, _ := reqctx.WithAccessFields(httptest.NewRequest(http.MethodGet, "/x", nil).Context())
	req := httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(ctx)

	Metrics()(next).ServeHTTP(httptest.NewRecorder(), req)

	if got := testutil.ToFloat64(metrics.RequestsTotal.WithLabelValues("/mw-metrics-test/", "201")); got != 1 {
		t.Errorf("RequestsTotal = %v, want 1", got)
	}
	if count := testutil.CollectAndCount(metrics.RequestDuration); count == 0 {
		t.Error("RequestDuration: expected at least one observation")
	}
}

func TestMetrics_DefaultsStatusTo200(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	ctx, _ := reqctx.WithAccessFields(httptest.NewRequest(http.MethodGet, "/x", nil).Context())
	req := httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(ctx)

	Metrics()(next).ServeHTTP(httptest.NewRecorder(), req)

	if got := testutil.ToFloat64(metrics.RequestsTotal.WithLabelValues("", "200")); got == 0 {
		t.Error("expected a request_total sample for the empty-route/200 case")
	}
}
