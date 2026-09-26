package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gatekeeper/internal/config"
	"gatekeeper/internal/gateway"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func testConfig(upstreamURL string) *config.Config {
	return &config.Config{
		Server: config.ServerConfig{Listen: ":0", ReadHeaderTimeout: config.Duration(5 * time.Second), MaxBodyBytes: config.ByteSize(1 << 20)},
		Upstreams: map[string]config.UpstreamConfig{
			"demo": {
				Balancer: config.BalancerRoundRobin,
				Targets:  []config.Target{{URL: upstreamURL, Weight: 1}},
				Timeout:  config.Duration(time.Second),
				HealthCheck: config.HealthCheckConfig{
					Path: "/", Interval: config.Duration(time.Hour), Timeout: config.Duration(time.Second),
					HealthyThreshold: 1, UnhealthyThreshold: 1,
				},
				CircuitBreaker: config.CircuitBreakerConfig{
					FailureRatio: 0.5, MinRequests: 1000, Window: config.Duration(10 * time.Second),
					OpenTimeout: config.Duration(time.Second), HalfOpenMax: 1,
				},
			},
		},
		Routes: []config.RouteConfig{
			{Match: config.MatchConfig{PathPrefix: "/api/demo/"}, Upstream: "demo", Auth: config.AuthNone},
		},
	}
}

func newTestGateway(t *testing.T) *gateway.Gateway {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	snap, err := gateway.Build(ctx, testConfig(srv.URL), testLogger(), nil)
	if err != nil {
		t.Fatalf("gateway.Build: unexpected error: %v", err)
	}
	gw := gateway.New()
	gw.Swap(snap)
	return gw
}

const testToken = "test-admin-token"

func TestHandleHealthz_AlwaysOK(t *testing.T) {
	h := NewHandler(gateway.New(), func() error { return nil }, testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestHandleReadyz_NotReadyBeforeSnapshot(t *testing.T) {
	h := NewHandler(gateway.New(), func() error { return nil }, testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestHandleReadyz_ReadyAfterSnapshot(t *testing.T) {
	gw := newTestGateway(t)
	h := NewHandler(gw, func() error { return nil }, testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestHandleMetrics_NoAuthNeeded(t *testing.T) {
	h := NewHandler(gateway.New(), func() error { return nil }, testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.Len() == 0 {
		t.Error("expected a non-empty Prometheus exposition body")
	}
}

func TestAdminRoutes_RequiresBearerToken(t *testing.T) {
	gw := newTestGateway(t)
	h := NewHandler(gw, func() error { return nil }, testToken)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/routes", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no header: status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/routes", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: status = %d, want %d", rec2.Code, http.StatusUnauthorized)
	}
}

func TestAdminRoutes_ReturnsConfiguredRoutes(t *testing.T) {
	gw := newTestGateway(t)
	h := NewHandler(gw, func() error { return nil }, testToken)

	req := httptest.NewRequest(http.MethodGet, "/admin/routes", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var routes []config.RouteConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &routes); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if len(routes) != 1 || routes[0].Upstream != "demo" || routes[0].Match.PathPrefix != "/api/demo/" {
		t.Errorf("routes = %+v, want one route to upstream demo at /api/demo/", routes)
	}
}

func TestAdminUpstreams_ReturnsHealthAndBreakerState(t *testing.T) {
	gw := newTestGateway(t)
	h := NewHandler(gw, func() error { return nil }, testToken)

	req := httptest.NewRequest(http.MethodGet, "/admin/upstreams", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var upstreams []upstreamView
	if err := json.Unmarshal(rec.Body.Bytes(), &upstreams); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if len(upstreams) != 1 || upstreams[0].Name != "demo" {
		t.Fatalf("upstreams = %+v, want one upstream named demo", upstreams)
	}
	if upstreams[0].BreakerState != "closed" {
		t.Errorf("BreakerState = %q, want %q", upstreams[0].BreakerState, "closed")
	}
	if len(upstreams[0].Targets) != 1 || !upstreams[0].Targets[0].Healthy {
		t.Errorf("Targets = %+v, want one healthy target", upstreams[0].Targets)
	}
}

func TestAdminReload_RequiresBearerToken(t *testing.T) {
	h := NewHandler(gateway.New(), func() error { return nil }, testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/reload", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestAdminReload_CallsReloadAndReportsSuccess(t *testing.T) {
	called := false
	h := NewHandler(gateway.New(), func() error { called = true; return nil }, testToken)

	req := httptest.NewRequest(http.MethodPost, "/admin/reload", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !called {
		t.Error("reload callback was not invoked")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestAdminReload_ReportsFailure(t *testing.T) {
	h := NewHandler(gateway.New(), func() error { return errors.New("boom") }, testToken)

	req := httptest.NewRequest(http.MethodPost, "/admin/reload", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if body["status"] != "error" || body["error"] != "boom" {
		t.Errorf("body = %+v, want status=error error=boom", body)
	}
}

func TestAdminReload_WrongMethodRejected(t *testing.T) {
	h := NewHandler(gateway.New(), func() error { return nil }, testToken)
	req := httptest.NewRequest(http.MethodGet, "/admin/reload", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
