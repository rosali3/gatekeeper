// Package ratelimit implements gatekeeper's rate limiting: a local
// in-memory token bucket per key, and a Redis-backed one for coordinating
// across multiple gateway instances.
package ratelimit

import (
	"context"
	"time"
)

// Clock is time as seen by a limiter. Injected so tests can advance time
// deterministically instead of calling time.Sleep.
type Clock interface {
	Now() time.Time
}

// RealClock is the production Clock, backed by the wall clock.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

// Config is one route's rate_limit.rps/burst, common to every backend.
type Config struct {
	RPS   float64
	Burst int
}

// Result carries both the allow/deny decision and everything needed to
// populate the RateLimit-*/Retry-After response headers.
type Result struct {
	Allowed bool
	// Limit is the bucket's capacity (rate_limit.burst).
	Limit int
	// Remaining is how many requests could still be made right now.
	Remaining int
	// ResetIn is how long until the bucket refills to full capacity.
	ResetIn time.Duration
	// RetryAfter is set when !Allowed: how long until at least one token
	// is available.
	RetryAfter time.Duration
}

// Limiter decides whether a request identified by key is allowed. Local
// never errors; the Redis-backed implementation can, e.g. if Redis is
// unreachable and the route is configured fail-closed.
type Limiter interface {
	Allow(ctx context.Context, key string) (Result, error)
}
