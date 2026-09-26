package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gatekeeper/internal/config"
)

func configWithRateLimit(upstreamURL string, rl config.RateLimitConfig) *config.Config {
	cfg := baseConfig(upstreamURL)
	route := cfg.Routes[0]
	route.RateLimit = &rl
	cfg.Routes[0] = route
	return cfg
}

func TestGateway_RateLimit_LocalBackendDeniesOverBurst(t *testing.T) {
	srv := backend(t, "demo-backend")
	cfg := configWithRateLimit(srv.URL, config.RateLimitConfig{
		Key: config.RateLimitKeyIP, RPS: 1, Burst: 1, Backend: config.RateLimitBackendLocal,
	})

	snap := buildSnapshot(t, cfg)
	gw := New()
	gw.Swap(snap)

	req := httptest.NewRequest(http.MethodGet, "/api/demo/x", nil)
	req.RemoteAddr = "203.0.113.9:1234"

	rec1 := httptest.NewRecorder()
	gw.ServeHTTP(rec1, req)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want %d", rec1.Code, http.StatusOK)
	}

	rec2Req := httptest.NewRequest(http.MethodGet, "/api/demo/x", nil)
	rec2Req.RemoteAddr = "203.0.113.9:5678" // same IP, different port -> same rate-limit key
	rec2 := httptest.NewRecorder()
	gw.ServeHTTP(rec2, rec2Req)

	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want %d", rec2.Code, http.StatusTooManyRequests)
	}
	if got := rec2.Header().Get("Retry-After"); got == "" {
		t.Error("429 response missing Retry-After header")
	}
}

func TestGateway_RateLimit_HeadersOnAllowedRequest(t *testing.T) {
	srv := backend(t, "demo-backend")
	cfg := configWithRateLimit(srv.URL, config.RateLimitConfig{
		Key: config.RateLimitKeyIP, RPS: 5, Burst: 10, Backend: config.RateLimitBackendLocal,
	})

	snap := buildSnapshot(t, cfg)
	gw := New()
	gw.Swap(snap)

	req := httptest.NewRequest(http.MethodGet, "/api/demo/x", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("RateLimit-Limit"); got != "10" {
		t.Errorf("RateLimit-Limit = %q, want %q", got, "10")
	}
	if got := rec.Header().Get("RateLimit-Remaining"); got != "9" {
		t.Errorf("RateLimit-Remaining = %q, want %q", got, "9")
	}
	if got := rec.Header().Get("RateLimit-Reset"); got == "" {
		t.Error("RateLimit-Reset header missing")
	}
}

func TestGateway_RateLimit_DifferentIPsAreIndependent(t *testing.T) {
	srv := backend(t, "demo-backend")
	cfg := configWithRateLimit(srv.URL, config.RateLimitConfig{
		Key: config.RateLimitKeyIP, RPS: 1, Burst: 1, Backend: config.RateLimitBackendLocal,
	})

	snap := buildSnapshot(t, cfg)
	gw := New()
	gw.Swap(snap)

	for _, ip := range []string{"203.0.113.1:1", "203.0.113.2:1"} {
		req := httptest.NewRequest(http.MethodGet, "/api/demo/x", nil)
		req.RemoteAddr = ip
		rec := httptest.NewRecorder()
		gw.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("request from %s: status = %d, want %d", ip, rec.Code, http.StatusOK)
		}
	}
}

func TestGateway_RateLimit_RoutesWithoutRateLimitAreUnaffected(t *testing.T) {
	srv := backend(t, "demo-backend")
	cfg := baseConfig(srv.URL) // no RateLimit set on the route

	snap := buildSnapshot(t, cfg)
	gw := New()
	gw.Swap(snap)

	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		gw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/demo/x", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("request #%d: status = %d, want %d (no rate limit configured)", i, rec.Code, http.StatusOK)
		}
	}
}

func TestGateway_RateLimit_APIKeyBackendUsesHeader(t *testing.T) {
	srv := backend(t, "demo-backend")
	cfg := configWithRateLimit(srv.URL, config.RateLimitConfig{
		Key: config.RateLimitKeyAPIKey, RPS: 1, Burst: 1, Backend: config.RateLimitBackendLocal,
	})

	snap := buildSnapshot(t, cfg)
	gw := New()
	gw.Swap(snap)

	req1 := httptest.NewRequest(http.MethodGet, "/api/demo/x", nil)
	req1.Header.Set("X-API-Key", "key-a")
	rec1 := httptest.NewRecorder()
	gw.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("key-a request status = %d, want %d", rec1.Code, http.StatusOK)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/demo/x", nil)
	req2.Header.Set("X-API-Key", "key-b")
	rec2 := httptest.NewRecorder()
	gw.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("key-b request status = %d, want %d (independent bucket from key-a)", rec2.Code, http.StatusOK)
	}

	req3 := httptest.NewRequest(http.MethodGet, "/api/demo/x", nil)
	req3.Header.Set("X-API-Key", "key-a")
	rec3 := httptest.NewRecorder()
	gw.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusTooManyRequests {
		t.Fatalf("second key-a request status = %d, want %d", rec3.Code, http.StatusTooManyRequests)
	}
}

func TestBuild_RateLimitJWTSubNotImplemented(t *testing.T) {
	cfg := configWithRateLimit("http://127.0.0.1:1", config.RateLimitConfig{
		Key: config.RateLimitKeyJWTSub, RPS: 1, Burst: 1, Backend: config.RateLimitBackendLocal,
	})

	if _, err := Build(context.Background(), cfg, testLogger(), nil); err == nil {
		t.Fatal("Build: expected an error for rate_limit.key: jwt_sub, got nil")
	}
}

func TestBuild_RateLimitJWTSubFailsBeforeStartingGoroutines(t *testing.T) {
	// Two upstreams so a real bug (starting goroutines before validating
	// every route) would have something to leak; TestMain's goleak check
	// covers the actual leak detection.
	srv := backend(t, "demo-backend")
	cfg := configWithRateLimit(srv.URL, config.RateLimitConfig{
		Key: config.RateLimitKeyJWTSub, RPS: 1, Burst: 1, Backend: config.RateLimitBackendLocal,
	})
	cfg.Upstreams["other"] = config.UpstreamConfig{
		Balancer:    config.BalancerRoundRobin,
		Targets:     []config.Target{{URL: srv.URL, Weight: 1}},
		Timeout:     config.Duration(time.Second),
		HealthCheck: testHealthCheck(),
	}

	if _, err := Build(context.Background(), cfg, testLogger(), nil); err == nil {
		t.Fatal("Build: expected an error, got nil")
	}
}
