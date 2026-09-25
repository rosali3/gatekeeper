package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// tokenBucketScript is a token bucket, same algorithm as Local, but
// evaluated atomically inside Redis so multiple gateway instances share
// one limit correctly under concurrency.
//
// Token bucket was chosen over a sliding-window log (the TZ's other
// suggested option): a sliding window log needs one sorted-set entry per
// request to compute a precise count, which is much more Redis memory and
// CPU per request than two numbers in a hash. Token bucket needs O(1)
// state per key regardless of request rate, at the cost of allowing
// short bursts up to the bucket size - an acceptable trade for a gateway
// rate limiter, and it matches the same semantics/headers as the local
// backend, so switching backends doesn't change client-visible behavior.
//
// KEYS[1] = bucket key
// ARGV[1] = rps, ARGV[2] = burst, ARGV[3] = now (unix seconds, float)
// Returns {allowed (0/1), tokens remaining (float string), ttl seconds}
const tokenBucketScript = `
local key = KEYS[1]
local rps = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local now = tonumber(ARGV[3])

local state = redis.call("HMGET", key, "tokens", "ts")
local tokens = tonumber(state[1])
local ts = tonumber(state[2])
if tokens == nil then
  tokens = burst
  ts = now
end

local elapsed = math.max(0, now - ts)
tokens = math.min(burst, tokens + elapsed * rps)

local allowed = 0
if tokens >= 1 then
  allowed = 1
  tokens = tokens - 1
end

local ttl = math.ceil(burst / rps) + 1
redis.call("HSET", key, "tokens", tostring(tokens), "ts", tostring(now))
redis.call("EXPIRE", key, ttl)

return { allowed, tostring(tokens), ttl }
`

// Redis is a distributed token bucket limiter backed by a shared Redis
// instance, for coordinating a limit across multiple gateway processes.
type Redis struct {
	client    redis.UniversalClient
	clock     Clock
	cfg       Config
	script    *redis.Script
	failOpen  bool
	keyPrefix string
}

// NewRedis builds a Redis-backed limiter. failOpen controls what Allow
// returns when Redis itself is unreachable: true lets the request through
// (availability over strictness), false denies it (strictness over
// availability) - this mirrors rate_limit.fail_open in the config.
func NewRedis(client redis.UniversalClient, clock Clock, cfg Config, keyPrefix string, failOpen bool) *Redis {
	return &Redis{
		client:    client,
		clock:     clock,
		cfg:       cfg,
		script:    redis.NewScript(tokenBucketScript),
		failOpen:  failOpen,
		keyPrefix: keyPrefix,
	}
}

func (r *Redis) Allow(ctx context.Context, key string) (Result, error) {
	now := float64(r.clock.Now().UnixNano()) / float64(time.Second)

	raw, err := r.script.Run(ctx, r.client, []string{r.keyPrefix + key}, r.cfg.RPS, r.cfg.Burst, now).Result()
	if err != nil {
		if r.failOpen {
			return Result{Allowed: true, Limit: r.cfg.Burst, Remaining: r.cfg.Burst}, nil
		}
		return Result{}, fmt.Errorf("redis rate limit unavailable (fail-closed): %w", err)
	}

	values, ok := raw.([]interface{})
	if !ok || len(values) != 3 {
		return Result{}, errors.New("redis rate limit: unexpected script response shape")
	}
	allowed := values[0].(int64) == 1
	var tokens float64
	if _, err := fmt.Sscanf(values[1].(string), "%g", &tokens); err != nil {
		return Result{}, fmt.Errorf("redis rate limit: parsing tokens: %w", err)
	}
	ttl := values[2].(int64)

	res := Result{
		Allowed:   allowed,
		Limit:     r.cfg.Burst,
		Remaining: int(tokens),
		ResetIn:   time.Duration(ttl) * time.Second,
	}
	if !allowed {
		needed := 1 - tokens
		res.RetryAfter = time.Duration(needed / r.cfg.RPS * float64(time.Second))
	}
	return res, nil
}
