//go:build integration

// Redis-backed tests need a real Redis (the whole point is testing the Lua
// script atomicity), so they're gated behind the "integration" build tag -
// `go test ./...` (what CI's main test step and `make test` run) never
// needs Redis; `make test-integration` / `go test -tags=integration ./...`
// does. CI runs a redis service container for this.
package ratelimit

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func redisAddr() string {
	if addr := os.Getenv("REDIS_ADDR"); addr != "" {
		return addr
	}
	return "localhost:6379"
}

func newTestRedisClient(t *testing.T) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: redisAddr()})
	t.Cleanup(func() { client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("redis not reachable at %s: %v", redisAddr(), err)
	}
	return client
}

func TestRedis_AllowsUpToBurstThenDenies(t *testing.T) {
	client := newTestRedisClient(t)
	clock := newFakeClock(epoch)
	key := uniqueKey(t)

	r := NewRedis(client, clock, Config{RPS: 1, Burst: 3}, "ratelimit_test:", false)

	for i := 0; i < 3; i++ {
		res, err := r.Allow(context.Background(), key)
		if err != nil {
			t.Fatalf("Allow() #%d: unexpected error: %v", i, err)
		}
		if !res.Allowed {
			t.Fatalf("Allow() #%d: denied, want allowed (within burst)", i)
		}
	}

	res, err := r.Allow(context.Background(), key)
	if err != nil {
		t.Fatalf("Allow() #4: unexpected error: %v", err)
	}
	if res.Allowed {
		t.Fatal("Allow() #4: allowed, want denied (burst exhausted)")
	}
	if res.RetryAfter <= 0 {
		t.Errorf("RetryAfter = %v, want > 0 when denied", res.RetryAfter)
	}
}

func TestRedis_RefillsOverTime(t *testing.T) {
	client := newTestRedisClient(t)
	clock := newFakeClock(epoch)
	key := uniqueKey(t)

	r := NewRedis(client, clock, Config{RPS: 1, Burst: 1}, "ratelimit_test:", false)

	if res, _ := r.Allow(context.Background(), key); !res.Allowed {
		t.Fatal("first request should be allowed (bucket starts full)")
	}
	if res, _ := r.Allow(context.Background(), key); res.Allowed {
		t.Fatal("second immediate request should be denied")
	}

	clock.Advance(time.Second)

	res, err := r.Allow(context.Background(), key)
	if err != nil {
		t.Fatalf("Allow: unexpected error: %v", err)
	}
	if !res.Allowed {
		t.Fatal("request after refill should be allowed")
	}
}

// TestRedis_SharedAcrossTwoLimiterInstances is the actual point of a
// Redis-backed limiter: two independent *Redis (standing in for two
// gateway processes) pointed at the same Redis must share one limit.
func TestRedis_SharedAcrossTwoLimiterInstances(t *testing.T) {
	client := newTestRedisClient(t)
	clock := newFakeClock(epoch)
	key := uniqueKey(t)
	cfg := Config{RPS: 1, Burst: 2}

	gatewayA := NewRedis(client, clock, cfg, "ratelimit_test:", false)
	gatewayB := NewRedis(client, clock, cfg, "ratelimit_test:", false)

	if res, _ := gatewayA.Allow(context.Background(), key); !res.Allowed {
		t.Fatal("gatewayA request #1 should be allowed")
	}
	if res, _ := gatewayB.Allow(context.Background(), key); !res.Allowed {
		t.Fatal("gatewayB request #1 should be allowed (shares the burst with A)")
	}
	// Burst of 2 is now exhausted across both instances combined.
	res, _ := gatewayA.Allow(context.Background(), key)
	if res.Allowed {
		t.Fatal("gatewayA request #2 should be denied: the limit is shared, not per-instance")
	}
}

func TestRedis_FailOpenOnUnreachableRedis(t *testing.T) {
	clock := newFakeClock(epoch)
	unreachable := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 200 * time.Millisecond, MaxRetries: -1})
	defer unreachable.Close()

	r := NewRedis(unreachable, clock, Config{RPS: 1, Burst: 1}, "ratelimit_test:", true)
	res, err := r.Allow(context.Background(), uniqueKey(t))
	if err != nil {
		t.Fatalf("fail-open: expected no error, got %v", err)
	}
	if !res.Allowed {
		t.Error("fail-open: expected the request to be allowed when Redis is unreachable")
	}
}

func TestRedis_FailClosedOnUnreachableRedis(t *testing.T) {
	clock := newFakeClock(epoch)
	unreachable := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 200 * time.Millisecond, MaxRetries: -1})
	defer unreachable.Close()

	r := NewRedis(unreachable, clock, Config{RPS: 1, Burst: 1}, "ratelimit_test:", false)
	_, err := r.Allow(context.Background(), uniqueKey(t))
	if err == nil {
		t.Error("fail-closed: expected an error when Redis is unreachable")
	}
}

func uniqueKey(t *testing.T) string {
	t.Helper()
	return t.Name() + "-" + time.Now().Format("150405.000000000")
}
