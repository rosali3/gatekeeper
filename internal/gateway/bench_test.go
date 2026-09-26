package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"gatekeeper/internal/config"
)

func benchBackend(b *testing.B) *httptest.Server {
	b.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	b.Cleanup(srv.Close)
	return srv
}

func benchSnapshot(b *testing.B, cfg *config.Config) *Snapshot {
	b.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	b.Cleanup(cancel)
	snap, err := Build(ctx, cfg, testLogger(), nil)
	if err != nil {
		b.Fatalf("Build: %v", err)
	}
	return snap
}

// BenchmarkGateway_ServeHTTP is the actual "gateway overhead" number: the
// full hot path (recover, request-id, access-log, metrics, tracing,
// CORS, body-limit, route-match, auth, rate-limit, circuit-breaker,
// balancer pick, proxy) against a local backend. ReportAllocs surfaces
// allocations/op directly in `go test -bench=. -benchmem` output.
func BenchmarkGateway_ServeHTTP(b *testing.B) {
	srv := benchBackend(b)
	gw := New()
	gw.Swap(benchSnapshot(b, baseConfig(srv.URL)))

	req := httptest.NewRequest(http.MethodGet, "/api/demo/x", nil)

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		gw.ServeHTTP(httptest.NewRecorder(), req)
	}
}

func BenchmarkGateway_ServeHTTP_Parallel(b *testing.B) {
	srv := benchBackend(b)
	gw := New()
	gw.Swap(benchSnapshot(b, baseConfig(srv.URL)))

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/api/demo/x", nil)
		for pb.Next() {
			gw.ServeHTTP(httptest.NewRecorder(), req)
		}
	})
}

// BenchmarkGateway_ServeHTTP_WithLocalRateLimit adds a local token-bucket
// check to the hot path, isolating its incremental cost.
func BenchmarkGateway_ServeHTTP_WithLocalRateLimit(b *testing.B) {
	srv := benchBackend(b)
	cfg := baseConfig(srv.URL)
	route := cfg.Routes[0]
	route.RateLimit = &config.RateLimitConfig{
		Key: config.RateLimitKeyIP, RPS: 1e9, Burst: 1_000_000, Backend: config.RateLimitBackendLocal,
	}
	cfg.Routes[0] = route

	gw := New()
	gw.Swap(benchSnapshot(b, cfg))

	req := httptest.NewRequest(http.MethodGet, "/api/demo/x", nil)

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		gw.ServeHTTP(httptest.NewRecorder(), req)
	}
}
