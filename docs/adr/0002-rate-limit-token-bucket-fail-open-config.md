# ADR 0002: Token bucket (not sliding-window log) for rate limiting; fail-open/closed is config-driven

## Status
Accepted

## Context
The TZ leaves the exact algorithm open ("token bucket или sliding
window log — выбрать и обосновать") for both the local and Redis-backed
limiters, and separately requires that Redis-unavailable behavior be
configurable rather than hardcoded.

## Decision
**Token bucket**, for both backends, with the same semantics
(`internal/ratelimit.Config{RPS, Burst}`) so switching `backend: local`
↔ `backend: redis` in config doesn't change client-visible behavior.

A sliding-window log (store every request's timestamp, count how many
fall in the last N seconds) gives an exact count, but at O(1) memory
per request - for the Redis backend specifically, that's one sorted-set
entry per request, which is both more Redis memory and more CPU
(`ZREMRANGEBYSCORE` + `ZCARD` on every check) than token bucket's O(1)
state per key regardless of request rate (two numbers in a hash:
`tokens`, `ts`). Token bucket trades exactness for that - it allows a
short burst up to `burst` tokens even after a quiet period - which is
exactly the point of having a `burst` parameter at all, not really a
downside.

Local and Redis share the algorithm but not the implementation: the
local limiter (`internal/ratelimit/local.go`) shards by key hash and
runs a janitor goroutine to evict idle buckets, since it's holding
real Go memory; the Redis limiter (`redis.go`) is stateless on the Go
side and pushes the equivalent logic into a Lua script
(`tokenBucketScript`) so a single Redis round trip does the whole
read-refill-check-write atomically - a naive GET-then-SET from Go would
race across multiple gateway instances.

**Fail-open/closed** is `rate_limit.fail_open` in the route config
(defaults to `false`, i.e. fail-closed, when omitted). The TZ calls
this out explicitly as something that must be config-driven rather
than a fixed choice, presumably because the right answer genuinely
depends on the deployment: fail-open favors availability (Redis outage
shouldn't take down every rate-limited route), fail-closed favors
correctness/protection (a struggling downstream is exactly when you
don't want rate limiting to silently disappear). Defaulting to
fail-closed when unspecified is the more conservative failure mode.

## Consequences
- The Lua script is the actual atomicity boundary for the distributed
  limiter - verified directly in `internal/ratelimit/redis_test.go` by
  running two independent `*Redis` instances (standing in for two
  gateway processes) against the same key and confirming the burst is
  shared, not doubled.
- Both limiters use an injected `Clock` interface (`Now() time.Time`,
  a `RealClock` in production and a `fakeClock` in tests with an
  `Advance(d)` method) so tests move time forward explicitly instead of
  calling `time.Sleep` and hoping the scheduler cooperates - a bucket's
  refill math is entirely a function of elapsed time, so this makes
  "advance 1 second, expect exactly 1 more token" a deterministic
  assertion instead of a flaky one. `internal/breaker` repeats the same
  small interface for the same reason, rather than sharing one package
  for a 3-line type - not worth the coupling.
