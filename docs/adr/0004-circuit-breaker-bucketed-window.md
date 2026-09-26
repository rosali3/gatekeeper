# ADR 0004: Circuit breaker with a bucketed sliding window, not a raw counter or per-request log

## Status
Accepted

## Context
A circuit breaker needs to answer "what fraction of recent requests to
this upstream failed?" - "recent" meaning a trailing time window, not
"since the process started" (a target that failed constantly an hour
ago but has been fine since shouldn't still count against it) and not
"the last N requests" (that conflates request *rate* with time,
which produces different trip sensitivity depending on how busy the
upstream currently is).

## Decision
Split the configured `window` into 10 fixed-size buckets
(`internal/breaker/breaker.go`'s `numBuckets`). Each bucket records
successes/failures for one `window/10` slice of time; a bucket is
"valid" (counted toward the running ratio) only while
`now - bucket.windowStart <= window`, checked at read time rather than
by a background sweep. Recording is O(1) (index by `now`, reset the
slot if it's stale, increment); computing the current ratio sums at
most 10 buckets. Memory is fixed regardless of request rate - the
alternative (one timestamped entry per request, like a sliding-window
*log*) would grow unboundedly under load, which is precisely the
failure mode a circuit breaker exists to guard against elsewhere in the
system.

State transitions (`closed -> open -> half-open`) live in
`CircuitBreaker` itself, gated by `minRequests` (don't trip on 1 failure
out of 1 sample) and `halfOpenMax` (limit how many probe requests get
let through before deciding to close or reopen). Any single failure
during a half-open probation reopens immediately rather than averaging
it in - a struggling upstream that fails even once while being
carefully tested with a *reduced* load isn't ready for full traffic.

## Consequences
- `RecordResult` resets the whole bucket array on a half-open -> closed
  transition. Without that, a target that tripped open, recovered, and
  closed again could immediately re-trip on the *old* window's stale
  failures the moment one more failure landed - `TestCircuitBreaker_
  ClosingResetsTheWindow` exists specifically to pin this down after it
  came up during design.
- The same `bucketIndex`/`bucketValid` helpers back `RetryBudget`
  (`internal/breaker/budget.go`), which counts (requests, retries)
  instead of (successes, failures) over its own fixed window - same
  bucketing technique, different question being asked of it.
- `Allow()` is the actual short-circuit: called *before* proxying, so
  an open breaker returns `503` without a dial attempt, not after one
  times out.
