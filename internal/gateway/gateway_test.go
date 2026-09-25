package gateway

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/goleak"

	"gatekeeper/internal/config"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// backend returns an httptest.Server that echoes its own name and the
// request path, so tests can tell which backend and which rewritten path a
// request actually reached. It also answers any path with 200, so it
// doubles as a valid active-health-check target without extra wiring.
func backend(t *testing.T, name string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", name)
		w.Header().Set("X-Received-Path", r.URL.Path)
		w.Header().Set("X-Received-Host", r.Host)
		w.Header().Set("X-Forwarded-For", r.Header.Get("X-Forwarded-For"))
		w.Header().Set("X-Received-User-ID", r.Header.Get("X-User-ID"))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testHealthCheck() config.HealthCheckConfig {
	return config.HealthCheckConfig{
		Path:               "/",
		Interval:           config.Duration(50 * time.Millisecond),
		Timeout:            config.Duration(100 * time.Millisecond),
		HealthyThreshold:   1,
		UnhealthyThreshold: 1,
	}
}

func baseConfig(upstreamURL string) *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Listen:            ":0",
			ReadHeaderTimeout: config.Duration(5 * time.Second),
			MaxBodyBytes:      config.ByteSize(1 << 20),
		},
		Upstreams: map[string]config.UpstreamConfig{
			"demo": {
				Balancer:    config.BalancerRoundRobin,
				Targets:     []config.Target{{URL: upstreamURL, Weight: 1}},
				Timeout:     config.Duration(2 * time.Second),
				HealthCheck: testHealthCheck(),
			},
		},
		Routes: []config.RouteConfig{
			{
				Match:       config.MatchConfig{PathPrefix: "/api/demo/"},
				StripPrefix: "/api/demo",
				Upstream:    "demo",
				Auth:        config.AuthNone,
			},
		},
		CORS: config.CORSConfig{AllowedOrigins: []string{"http://localhost:3000"}},
	}
}

// buildSnapshot builds cfg with a context this test cancels on cleanup, so
// the health-check goroutines Build starts don't outlive the test (checked
// by TestMain's goleak verification).
func buildSnapshot(t *testing.T, cfg *config.Config) *Snapshot {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	snap, err := Build(ctx, cfg, testLogger())
	if err != nil {
		t.Fatalf("Build: unexpected error: %v", err)
	}
	return snap
}

func TestGateway_ProxiesMatchedRoute(t *testing.T) {
	srv := backend(t, "demo-backend")
	cfg := baseConfig(srv.URL)

	snap := buildSnapshot(t, cfg)
	gw := New()
	gw.Swap(snap)

	req := httptest.NewRequest(http.MethodGet, "/api/demo/widgets", nil)
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("X-Backend"); got != "demo-backend" {
		t.Errorf("X-Backend = %q, want %q", got, "demo-backend")
	}
	if got := rec.Header().Get("X-Received-Path"); got != "/widgets" {
		t.Errorf("received path = %q, want %q (strip_prefix applied)", got, "/widgets")
	}
}

func TestGateway_UnmatchedRouteIs404(t *testing.T) {
	srv := backend(t, "demo-backend")
	cfg := baseConfig(srv.URL)

	snap := buildSnapshot(t, cfg)
	gw := New()
	gw.Swap(snap)

	req := httptest.NewRequest(http.MethodGet, "/nope", nil)
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestGateway_SetsRequestIDAndForwardedFor(t *testing.T) {
	srv := backend(t, "demo-backend")
	cfg := baseConfig(srv.URL)

	snap := buildSnapshot(t, cfg)
	gw := New()
	gw.Swap(snap)

	req := httptest.NewRequest(http.MethodGet, "/api/demo/x", nil)
	req.RemoteAddr = "203.0.113.7:54321"
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Request-ID"); got == "" {
		t.Error("response missing X-Request-ID")
	}
	if got := rec.Header().Get("X-Forwarded-For"); got != "203.0.113.7" {
		t.Errorf("upstream saw X-Forwarded-For = %q, want %q", got, "203.0.113.7")
	}
}

func TestGateway_UpstreamTimeout(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(slow.Close)

	cfg := baseConfig(slow.URL)
	cfg.Upstreams["demo"] = config.UpstreamConfig{
		Balancer:    config.BalancerRoundRobin,
		Targets:     []config.Target{{URL: slow.URL, Weight: 1}},
		Timeout:     config.Duration(10 * time.Millisecond),
		HealthCheck: testHealthCheck(),
	}

	snap := buildSnapshot(t, cfg)
	gw := New()
	gw.Swap(snap)

	req := httptest.NewRequest(http.MethodGet, "/api/demo/x", nil)
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req)

	if rec.Code != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusGatewayTimeout)
	}
}

func TestGateway_NoSnapshotYet(t *testing.T) {
	gw := New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestBuild_UnknownBalancerFails(t *testing.T) {
	cfg := baseConfig("http://127.0.0.1:1")
	cfg.Upstreams["demo"] = config.UpstreamConfig{
		Balancer:    "bogus",
		Targets:     []config.Target{{URL: "http://127.0.0.1:1", Weight: 1}},
		Timeout:     config.Duration(time.Second),
		HealthCheck: testHealthCheck(),
	}

	// No goroutines are started on this path (Build validates every
	// balancer before starting any health checker), so no context/cleanup
	// dance is needed here.
	if _, err := Build(context.Background(), cfg, testLogger()); err == nil {
		t.Fatal("Build: expected error for an unknown balancer, got nil")
	}
}

func TestGateway_PassiveFailureMarksTargetUnhealthy(t *testing.T) {
	const refused = "http://127.0.0.1:1" // nothing listens here
	cfg := baseConfig(refused)
	cfg.Upstreams["demo"] = config.UpstreamConfig{
		Balancer: config.BalancerRoundRobin,
		Targets:  []config.Target{{URL: refused, Weight: 1}},
		Timeout:  config.Duration(200 * time.Millisecond),
		// A long interval keeps the active checker from also marking this
		// target down, so the transition below is unambiguously caused by
		// the passive path (the proxy's ErrorHandler).
		HealthCheck: config.HealthCheckConfig{
			Path: "/", Interval: config.Duration(time.Hour), Timeout: config.Duration(100 * time.Millisecond),
			HealthyThreshold: 1, UnhealthyThreshold: 1,
		},
	}

	snap := buildSnapshot(t, cfg)
	gw := New()
	gw.Swap(snap)
	target := snap.Upstreams["demo"].Balancer.Targets()[0]

	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/demo/x", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("first request status = %d, want %d (connection refused)", rec.Code, http.StatusBadGateway)
	}
	if target.Healthy() {
		t.Fatal("target should be unhealthy after one passive failure (unhealthyThreshold=1)")
	}

	// The balancer now has no healthy target at all, so the next request
	// should short-circuit to 503 instead of attempting to dial again.
	rec2 := httptest.NewRecorder()
	gw.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/api/demo/x", nil))
	if rec2.Code != http.StatusServiceUnavailable {
		t.Errorf("second request status = %d, want %d", rec2.Code, http.StatusServiceUnavailable)
	}
}
