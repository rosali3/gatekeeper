package ratelimit

import (
	"context"
	"hash/fnv"
	"math"
	"sync"
	"time"
)

const (
	shardCount      = 16
	defaultIdleTTL  = 5 * time.Minute
	janitorInterval = time.Minute
)

// bucket is one key's token-bucket state. tokens and lastRefill move
// together (refilling needs both), so they share a mutex rather than each
// being an atomic - unlike balancer.Target's fields, a bucket's state
// isn't read on every request across the whole gateway, just for its own
// key, so lock contention here is naturally already partitioned by key.
type bucket struct {
	mu         sync.Mutex
	tokens     float64
	lastRefill time.Time
}

// take applies elapsed-time refill then tries to spend one token,
// returning the resulting Result. now is passed in (rather than read from
// a clock here) so the caller's single clock.Now() call is also what
// decides idle-eviction consistency.
func (b *bucket) take(now time.Time, cfg Config) Result {
	b.mu.Lock()
	defer b.mu.Unlock()

	if elapsed := now.Sub(b.lastRefill).Seconds(); elapsed > 0 {
		b.tokens = math.Min(float64(cfg.Burst), b.tokens+elapsed*cfg.RPS)
		b.lastRefill = now
	}

	if b.tokens >= 1 {
		b.tokens--
		return Result{
			Allowed:   true,
			Limit:     cfg.Burst,
			Remaining: int(b.tokens),
			ResetIn:   durationUntilFull(b.tokens, cfg),
		}
	}
	needed := 1 - b.tokens
	return Result{
		Allowed:    false,
		Limit:      cfg.Burst,
		Remaining:  0,
		ResetIn:    durationUntilFull(b.tokens, cfg),
		RetryAfter: time.Duration(needed / cfg.RPS * float64(time.Second)),
	}
}

func durationUntilFull(tokens float64, cfg Config) time.Duration {
	missing := float64(cfg.Burst) - tokens
	if missing <= 0 {
		return 0
	}
	return time.Duration(missing / cfg.RPS * float64(time.Second))
}

// shard guards one slice of the overall key space, so keys hashing to
// different shards never contend on the same map lock.
type shard struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

// Local is an in-memory, per-key token bucket limiter, sharded to reduce
// lock contention and janitored so buckets for keys that stopped sending
// traffic don't accumulate forever.
type Local struct {
	clock   Clock
	cfg     Config
	idleTTL time.Duration
	shards  [shardCount]*shard
}

func NewLocal(clock Clock, cfg Config) *Local {
	l := &Local{clock: clock, cfg: cfg, idleTTL: defaultIdleTTL}
	for i := range l.shards {
		l.shards[i] = &shard{buckets: make(map[string]*bucket)}
	}
	return l
}

func (l *Local) shardFor(key string) *shard {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return l.shards[h.Sum32()%shardCount]
}

func (l *Local) Allow(_ context.Context, key string) (Result, error) {
	sh := l.shardFor(key)

	sh.mu.Lock()
	b, ok := sh.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(l.cfg.Burst), lastRefill: l.clock.Now()}
		sh.buckets[key] = b
	}
	sh.mu.Unlock()

	return b.take(l.clock.Now(), l.cfg), nil
}

// Run evicts idle buckets on a fixed interval until ctx is canceled. One
// Local instance backs one route, so this is a light loop: at most a
// handful of shards, each holding at most as many buckets as there are
// distinct rate-limit keys seen recently.
func (l *Local) Run(ctx context.Context) {
	ticker := time.NewTicker(janitorInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.evictIdle()
		}
	}
}

func (l *Local) evictIdle() {
	cutoff := l.clock.Now().Add(-l.idleTTL)
	for _, sh := range l.shards {
		sh.mu.Lock()
		for key, b := range sh.buckets {
			b.mu.Lock()
			stale := b.lastRefill.Before(cutoff)
			b.mu.Unlock()
			if stale {
				delete(sh.buckets, key)
			}
		}
		sh.mu.Unlock()
	}
}
