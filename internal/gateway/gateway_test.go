package gateway

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gatekeeper/internal/config"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// backend returns an httptest.Server that echoes its own name and the
// request path, so tests can tell which backend and which rewritten path a
// request actually reached.
func backend(t *testing.T, name string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", name)
		w.Header().Set("X-Received-Path", r.URL.Path)
		w.Header().Set("X-Received-Host", r.Host)
		w.Header().Set("X-Forwarded-For", r.Header.Get("X-Forwarded-For"))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
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
				Balancer: config.BalancerRoundRobin,
				Targets:  []config.Target{{URL: upstreamURL, Weight: 1}},
				Timeout:  config.Duration(2 * time.Second),
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

func TestGateway_ProxiesMatchedRoute(t *testing.T) {
	srv := backend(t, "demo-backend")
	cfg := baseConfig(srv.URL)

	snap, err := Build(cfg, testLogger())
	if err != nil {
		t.Fatalf("Build: unexpected error: %v", err)
	}
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

	snap, err := Build(cfg, testLogger())
	if err != nil {
		t.Fatalf("Build: unexpected error: %v", err)
	}
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

	snap, err := Build(cfg, testLogger())
	if err != nil {
		t.Fatalf("Build: unexpected error: %v", err)
	}
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
		Balancer: config.BalancerRoundRobin,
		Targets:  []config.Target{{URL: slow.URL, Weight: 1}},
		Timeout:  config.Duration(10 * time.Millisecond),
	}

	snap, err := Build(cfg, testLogger())
	if err != nil {
		t.Fatalf("Build: unexpected error: %v", err)
	}
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
		Balancer: "weighted_round_robin",
		Targets:  []config.Target{{URL: "http://127.0.0.1:1", Weight: 1}},
		Timeout:  config.Duration(time.Second),
	}

	if _, err := Build(cfg, testLogger()); err == nil {
		t.Fatal("Build: expected error for a not-yet-implemented balancer, got nil")
	}
}
