package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gatekeeper/internal/config"
)

// buildWithPrev is buildSnapshot's counterpart for reload tests: same
// context-cleanup behavior, but threads prev through to Build.
func buildWithPrev(t *testing.T, cfg *config.Config, prev *Snapshot) *Snapshot {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	snap, err := Build(ctx, cfg, testLogger(), prev)
	if err != nil {
		t.Fatalf("Build: unexpected error: %v", err)
	}
	return snap
}

func TestReload_UnchangedUpstreamPreservesBreakerState(t *testing.T) {
	bad, _ := statusBackend(t, http.StatusServiceUnavailable)

	cfg := configWithRetries(bad.URL,
		[]config.Target{{URL: bad.URL, Weight: 1}},
		config.RetriesConfig{Max: 0},
		config.CircuitBreakerConfig{
			FailureRatio: 0.5, MinRequests: 2, Window: config.Duration(10 * time.Second),
			OpenTimeout: config.Duration(time.Minute), HalfOpenMax: 1,
		},
	)

	snap1 := buildWithPrev(t, cfg, nil)
	gw := New()
	gw.Swap(snap1)

	// Trip the breaker open.
	for i := 0; i < 2; i++ {
		gw.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/demo/x", nil))
	}
	if snap1.Upstreams["demo"].breaker.State().String() != "open" {
		t.Fatal("setup: breaker should be open before reloading")
	}

	// Reload with byte-for-byte the same config.
	snap2 := buildWithPrev(t, cfg, snap1)
	gw.Swap(snap2)

	if snap2.Upstreams["demo"] != snap1.Upstreams["demo"] {
		t.Fatal("unchanged upstream config should reuse the exact same *Upstream")
	}
	if snap2.Upstreams["demo"].breaker.State().String() != "open" {
		t.Error("breaker state should have carried over across the reload")
	}

	// The gateway should still be short-circuiting without a fresh
	// request ever reaching the backend, proving the SAME breaker/target
	// state (not a freshly-closed one) is in effect after Swap.
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/demo/x", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status after reload = %d, want %d (breaker should still be open)", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestReload_ChangedUpstreamGetsFreshState(t *testing.T) {
	bad, _ := statusBackend(t, http.StatusServiceUnavailable)
	good, _ := statusBackend(t, http.StatusOK)

	breakerCfg := config.CircuitBreakerConfig{
		FailureRatio: 0.5, MinRequests: 2, Window: config.Duration(10 * time.Second),
		OpenTimeout: config.Duration(time.Minute), HalfOpenMax: 1,
	}
	cfg1 := configWithRetries(bad.URL, []config.Target{{URL: bad.URL, Weight: 1}}, config.RetriesConfig{Max: 0}, breakerCfg)

	snap1 := buildWithPrev(t, cfg1, nil)
	gw := New()
	gw.Swap(snap1)
	for i := 0; i < 2; i++ {
		gw.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/demo/x", nil))
	}
	if snap1.Upstreams["demo"].breaker.State().String() != "open" {
		t.Fatal("setup: breaker should be open before reloading")
	}

	// Reload pointing the same upstream at a different target - this is
	// a real config change, so it must NOT reuse the tripped breaker.
	cfg2 := configWithRetries(good.URL, []config.Target{{URL: good.URL, Weight: 1}}, config.RetriesConfig{Max: 0}, breakerCfg)
	snap2 := buildWithPrev(t, cfg2, snap1)
	gw.Swap(snap2)

	if snap2.Upstreams["demo"] == snap1.Upstreams["demo"] {
		t.Fatal("changed upstream config must not reuse the old *Upstream")
	}
	if snap2.Upstreams["demo"].breaker.State().String() != "closed" {
		t.Error("a freshly-built upstream should start with a closed breaker")
	}

	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/demo/x", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status after reload = %d, want %d (new target, fresh breaker)", rec.Code, http.StatusOK)
	}
}

func TestReload_RemovedUpstreamStopsCleanly(t *testing.T) {
	srv := backend(t, "demo-backend")
	cfg1 := baseConfig(srv.URL)

	snap1 := buildWithPrev(t, cfg1, nil)
	gw := New()
	gw.Swap(snap1)

	// Reload with the route/upstream removed entirely (an empty config
	// still needs at least a valid shape; use a config with no routes/
	// upstreams matching what Build accepts structurally - Build itself
	// doesn't enforce config.Validate's "at least one" rules, only
	// Validate does, and this test goes through Build directly).
	cfg2 := &config.Config{
		Server:    cfg1.Server,
		Upstreams: map[string]config.UpstreamConfig{},
		Routes:    nil,
	}
	snap2 := buildWithPrev(t, cfg2, snap1)
	gw.Swap(snap2)

	if len(snap2.Upstreams) != 0 {
		t.Errorf("len(Upstreams) = %d, want 0 after removing the only upstream", len(snap2.Upstreams))
	}
	// The old upstream's health checker goroutine must have been stopped
	// - goleak's TestMain check at the end of the package's test run is
	// what actually proves this; this just documents the expectation.
}

func TestReload_UnchangedRouteRateLimitPreservesBucketState(t *testing.T) {
	srv := backend(t, "demo-backend")
	cfg := configWithRateLimit(srv.URL, config.RateLimitConfig{
		Key: config.RateLimitKeyIP, RPS: 1, Burst: 2, Backend: config.RateLimitBackendLocal,
	})

	snap1 := buildWithPrev(t, cfg, nil)
	gw := New()
	gw.Swap(snap1)

	req := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/demo/x", nil)
		r.RemoteAddr = "203.0.113.55:1"
		return r
	}

	// Spend one of the two tokens in the burst.
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req())
	if rec.Code != http.StatusOK {
		t.Fatalf("setup request status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("RateLimit-Remaining"); got != "1" {
		t.Fatalf("setup: RateLimit-Remaining = %q, want %q", got, "1")
	}

	// Reload with the identical config.
	snap2 := buildWithPrev(t, cfg, snap1)
	gw.Swap(snap2)

	rec2 := httptest.NewRecorder()
	gw.ServeHTTP(rec2, req())
	if rec2.Code != http.StatusOK {
		t.Fatalf("post-reload request status = %d, want %d", rec2.Code, http.StatusOK)
	}
	if got := rec2.Header().Get("RateLimit-Remaining"); got != "0" {
		t.Errorf("RateLimit-Remaining after reload = %q, want %q (bucket state should carry over, not reset to a full burst)", got, "0")
	}
}

func TestReload_ChangedRouteRateLimitResets(t *testing.T) {
	srv := backend(t, "demo-backend")
	cfg1 := configWithRateLimit(srv.URL, config.RateLimitConfig{
		Key: config.RateLimitKeyIP, RPS: 1, Burst: 1, Backend: config.RateLimitBackendLocal,
	})

	snap1 := buildWithPrev(t, cfg1, nil)
	gw := New()
	gw.Swap(snap1)

	req := httptest.NewRequest(http.MethodGet, "/api/demo/x", nil)
	req.RemoteAddr = "203.0.113.56:1"
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup request status = %d, want %d", rec.Code, http.StatusOK)
	}

	// Reload with a different burst - a real config change, must not
	// reuse the exhausted bucket.
	cfg2 := configWithRateLimit(srv.URL, config.RateLimitConfig{
		Key: config.RateLimitKeyIP, RPS: 1, Burst: 5, Backend: config.RateLimitBackendLocal,
	})
	snap2 := buildWithPrev(t, cfg2, snap1)
	gw.Swap(snap2)

	req2 := httptest.NewRequest(http.MethodGet, "/api/demo/x", nil)
	req2.RemoteAddr = "203.0.113.56:1"
	rec2 := httptest.NewRecorder()
	gw.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("post-reload request status = %d, want %d", rec2.Code, http.StatusOK)
	}
	if got := rec2.Header().Get("RateLimit-Remaining"); got != "4" {
		t.Errorf("RateLimit-Remaining after changing burst = %q, want %q (fresh bucket at the new capacity)", got, "4")
	}
}
