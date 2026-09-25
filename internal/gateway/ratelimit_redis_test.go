//go:build integration

package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"gatekeeper/internal/config"
)

func redisAddr() string {
	if addr := os.Getenv("REDIS_ADDR"); addr != "" {
		return addr
	}
	return "localhost:6379"
}

func skipIfRedisUnreachable(t *testing.T) {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{Addr: redisAddr()})
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("redis not reachable at %s: %v", redisAddr(), err)
	}
}

// TestGateway_RateLimit_RedisBackendSharedAcrossTwoGateways is the actual
// point of the redis backend: two independent *gateway.Gateway instances
// (standing in for two gatekeeper processes behind a load balancer) must
// share one rate limit through Redis, unlike the local backend where each
// process has its own counters.
func TestGateway_RateLimit_RedisBackendSharedAcrossTwoGateways(t *testing.T) {
	skipIfRedisUnreachable(t)

	srv := backend(t, "demo-backend")
	cfg := configWithRateLimit(srv.URL, config.RateLimitConfig{
		Key: config.RateLimitKeyIP, RPS: 1, Burst: 2, Backend: config.RateLimitBackendRedis,
	})
	cfg.Redis.Addr = redisAddr()

	snapA := buildSnapshot(t, cfg)
	snapB := buildSnapshot(t, cfg)
	gwA, gwB := New(), New()
	gwA.Swap(snapA)
	gwB.Swap(snapB)

	clientIP := fmt.Sprintf("198.51.100.%d:1234", time.Now().UnixNano()%250+1)
	req := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/demo/x", nil)
		r.RemoteAddr = clientIP
		return r
	}

	recA := httptest.NewRecorder()
	gwA.ServeHTTP(recA, req())
	if recA.Code != http.StatusOK {
		t.Fatalf("gwA request #1 status = %d, want %d", recA.Code, http.StatusOK)
	}

	recB := httptest.NewRecorder()
	gwB.ServeHTTP(recB, req())
	if recB.Code != http.StatusOK {
		t.Fatalf("gwB request #1 status = %d, want %d (shares burst=2 with gwA)", recB.Code, http.StatusOK)
	}

	// Burst of 2 is now spent across both gateways combined.
	recA2 := httptest.NewRecorder()
	gwA.ServeHTTP(recA2, req())
	if recA2.Code != http.StatusTooManyRequests {
		t.Fatalf("gwA request #2 status = %d, want %d: the limit must be shared via Redis, not per-instance", recA2.Code, http.StatusTooManyRequests)
	}
}
