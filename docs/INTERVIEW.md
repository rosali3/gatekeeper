# Interview questions

Twenty-five questions a middle-Go interview might ask about this
project, with answers and pointers into the actual code - not
generic Go trivia, but questions this specific codebase can answer
concretely.

## Concurrency primitives

### 1. Why does `RoundRobin` use `atomic.Uint64` but `WeightedRoundRobin` uses a `sync.Mutex`?

`RoundRobin.Pick` (`internal/balancer/roundrobin.go`) only needs one
number - the next index - and `Add(1)` gives every caller a distinct,
strictly increasing value with no coordination beyond that single CPU
instruction. `WeightedRoundRobin.Pick` (`weighted_round_robin.go`) has
to read *every* entry's `currentWeight`, find the max, and update the
winner, as one atomic step - if two goroutines each read a stale
`currentWeight` and both think they're the winner, the smoothing
property breaks and two requests get the same target when they
shouldn't. A `sync.Mutex` is the honest way to say "this needs to
happen as one unit"; trying to do it lock-free would mean a CAS loop
retrying over multiple fields, which is more code and more subtly
wrong than the mutex. See ADR 0003.

### 2. Where in this codebase would you reach for a mutex over atomics, and why there specifically?

`internal/breaker.CircuitBreaker` and `internal/health.Tracker` both
use `sync.Mutex` for the same reason: recording a result touches
several related fields together (success/failure counters *and* the
derived state), and those fields must move as one unit or a reader
could observe an inconsistent combination. Contrast with
`balancer.Target.Healthy()`/`ActiveConns()` - single independent
values, read on every single request, where atomics avoid lock
contention on the hottest path in the whole gateway. The rule of thumb
this project uses: multiple fields with an invariant between them ->
mutex; one value, no cross-field invariant, hot path -> atomic.

### 3. `LeastConn.Pick` allocates a closure per call; `RoundRobin.Pick` doesn't. Why, and does it matter?

`LeastConn` needs a *release* callback that decrements the specific
target's counter once the request finishes - that's per-call state
(which target, which counter), so it has to be a fresh closure.
`RoundRobin`/`WeightedRoundRobin` have no per-request state to release,
so they return a single shared package-level `noop` function, avoiding
an allocation on every pick. Whether it matters: one small allocation
per least-conn pick is negligible next to a request's overall cost
(see the benchmark numbers in the README - the isolated balancer
benchmarks show 0-1 allocs/op either way), but it's a clean example of
not paying for something you don't need.

## Goroutine lifecycle and leaks

### 4. How does this project guarantee a config reload doesn't leak the goroutines it started?

Every background goroutine (a `health.Checker`, a local rate
limiter's janitor) is created with its own `context.CancelFunc`,
stored on the struct that owns it (`Upstream.stopHealthChecker`,
`routeRateLimit.stopJanitor`) rather than sharing one context per
config generation. On reload, `gateway.Build` calls that stop function
for anything in the *previous* snapshot that wasn't reused into the new
one. See ADR 0005 and `internal/gateway/reload_test.go`, which runs
under `goleak.VerifyTestMain` - if a goroutine leaked, the test suite
itself would fail, not just a manual check.

### 5. What's `goleak` actually checking, and where is it wired in?

`go.uber.org/goleak` snapshots running goroutines at the end of a test
run and fails if any unexpected ones remain (ignoring known-benign
runtime/testing ones). It's wired via `TestMain(m *testing.M) {
goleak.VerifyTestMain(m) }` in every package that starts a background
goroutine: `internal/health`, `internal/gateway`, `internal/ratelimit`
(implicitly, through gateway's tests), `internal/middleware` (for the
otel TracerProvider setup in tests), and `cmd/gatekeeper`. It's
explicitly a test-only dependency per the TZ - never imported by
production code.

### 6. `health.Checker.Run` ticks and checks every target on each tick. How do you know a slow target doesn't block the others, and that canceling the context doesn't leave one hanging?

Each tick fires one goroutine per target (`checkAll` in
`internal/health/checker.go`), joined with a `sync.WaitGroup` before
the *next* tick is scheduled - so targets are checked concurrently
within a tick, and by the time `Run`'s loop goes back to `select`,
every check from that tick has already finished. Each check's HTTP
request uses a context derived from `Run`'s own ctx with the
configured timeout, so canceling the parent (shutdown, or this
upstream being replaced on reload) aborts any in-flight check
immediately rather than waiting for it to time out naturally.

## `context.Context`

### 7. How does a client disconnecting propagate to the in-flight upstream call?

`proxyOnce`/`proxyHandler` (`internal/gateway/gateway.go`) derive the
outbound request's context from the *incoming* request's context via
`context.WithTimeout(r.Context(), up.Timeout)`. `net/http` cancels
`r.Context()` when the client connection closes; because the upstream
call's context is a child of that one, cancellation propagates
automatically - `http.Transport`'s in-flight `RoundTrip` sees the
context done and aborts the connection to the upstream too. No extra
plumbing needed; it falls out of using `context.WithTimeout` on the
right parent instead of `context.Background()`.

### 8. Where does this project use `context.Value`, and is that a code smell?

Three places, all request-scoped data flowing *down* through a
middleware chain that different layers can't otherwise share without
threading extra function parameters through every layer:
`reqctx.AccessFields` (a pointer downstream layers mutate, that the
outermost access-log middleware reads back after the chain returns),
the matched `*router.Route` and `*Upstream` (route-match hands these to
the auth/rate-limit/proxy layers below it), and the picked
`*balancer.Target` (the proxy handler hands this to `ReverseProxy`'s
`Rewrite`). All three are "control-flow adjacent" data for *this*
request, not general-purpose parameters - the usual argument against
`context.Value` (it hides dependencies, isn't type-safe) mostly applies
to using it as a substitute for normal function arguments, which none
of these are.

### 9. Why does the retry loop re-derive a fresh `context.WithTimeout` on every attempt instead of one timeout for the whole request?

Each attempt targets a potentially different backend (`proxyHandler`,
`internal/gateway/gateway.go`) and should each get the full configured
`upstream.timeout`, not a shrinking remainder - a slow first attempt
shouldn't leave a healthy second target starved of time to respond.
The overall request is still bounded (attempts × timeout, capped
further by `retries.max` and the retry budget), just not by a single
shared deadline.

## `net/http`/`ReverseProxy` internals

### 10. What does `httputil.ReverseProxy` give you for free that this project deliberately didn't reimplement?

Hop-by-hop header stripping (`Connection`, `Keep-Alive`,
`Transfer-Encoding`, etc. - removed from both the outbound request and
the inbound response, automatically, after `Rewrite` runs) and
`ProxyRequest.SetXForwarded()` (`X-Forwarded-For/Host/Proto`,
appending to any existing `X-Forwarded-For` rather than overwriting
it). See `internal/gateway/proxy.go`'s comment on `newReverseProxy` -
these are exactly the kind of "not the point of the exercise" pieces
ADR 0001 draws the library-vs-hand-rolled line around.

### 11. How does the gateway pick a *different* target on retry, given `ReverseProxy` is built once per upstream and reused across requests?

The target isn't fixed on the `*httputil.ReverseProxy` at all - it's
read out of the outbound request's context inside `Rewrite`
(`target := pr.In.Context().Value(targetContextKey{}).(*balancer.Target)`),
set fresh by `proxyHandler` immediately before each `ServeHTTP` call.
One shared `ReverseProxy` per upstream, but the target it proxies to is
decided per call, per attempt.

### 12. Why does the traceparent injection happen inside `Rewrite` rather than as its own middleware?

`Rewrite` runs once per *attempt* (including retries to a different
target), so it's the right place to attach `X-Forwarded-*` and
`traceparent` to whatever the outbound request currently is -
`middleware.Tracing` (which starts the span) runs once per *request*,
outside the retry loop entirely, and has no visibility into which
attempt is currently in flight. `otel.GetTextMapPropagator().Inject`
inside `Rewrite` reads the span that's already on `pr.Out`'s context
(inherited from the request context `Tracing` attached it to) and
writes an up-to-date `traceparent` header each time.

## Connection pooling / thundering herd / retry storms

### 13. This project sets `MaxIdleConnsPerHost: 100` on its shared `*http.Transport`. What does that actually control, and what would happen at 1,000 concurrent requests to one target?

It caps how many *idle* (already-established, currently unused)
connections to one host the transport keeps around for reuse - not a
hard cap on concurrent requests. At 1,000 concurrent requests to one
target, the transport opens as many connections as needed (no
`MaxConnsPerHost` is set, so that's unbounded); once requests finish,
only 100 of those connections are kept idle for the next burst; the
rest are closed. The real ceiling for reuse-under-load is this number -
too low, and a request-heavy period pays for a fresh TCP+TLS handshake
disproportionately often instead of reusing a warm connection.

### 14. What's a thundering herd, and where in this codebase was one specifically designed against?

Many concurrent operations all doing the same expensive work at once
because they all noticed the same triggering condition simultaneously.
`internal/auth/jwks.go`'s `JWKSClient.refresh` is the direct case: if
a `kid` isn't cached, *every* concurrent request verifying a token with
that `kid` would otherwise all fire an HTTP fetch to the JWKS endpoint
at once. It's solved with a generation counter (see the doc comment on
the `generation` field, and ADR-equivalent reasoning in the git history
around that commit): a caller captures the generation it observed
*before* deciding it needs a refresh, and only actually fetches if that
generation hasn't already moved on by the time it acquires `refreshMu`
- a plain "skip if refreshed within the last N ms" debounce was tried
first and rejected because it also skips a *later, unrelated* refresh
that happens to land inside that window (a second, genuinely new `kid`
going missing moments after an earlier refresh).

### 15. How does the retry budget prevent a retry storm, concretely?

Without it, an upstream at (say) a 50% failure rate with `retries.max:
2` triples effective load on it (every failure gets retried up to
twice), making the failure rate *worse*, which triggers more retries -
a feedback loop. `RetryBudget.AllowRetry` (`internal/breaker/
budget.go`) caps retries at `budget_ratio` of recent *request* volume
(not attempt count), so once that ratio is hit, further retries are
simply denied and the failure is returned as-is rather than amplified.
See ADR 0006.

### 16. The circuit breaker's `Allow()` is checked before *every* attempt in the retry loop, not just the first. Why does that matter?

If the first attempt's failure is enough to trip the breaker mid-loop
(imagine `minRequests` is low and this failure is the one that crosses
the ratio), the *next* attempt in the same request's retry loop should
see the now-open breaker and stop immediately - not proceed to dial a
target the breaker has just decided is unhealthy for the whole
upstream. Checking `Allow()` fresh on every attempt (rather than once
before the loop) is what makes that possible.

## Distributed rate limiting

### 17. Why is the Redis rate limiter's core logic a Lua script rather than a few Redis commands from Go?

A na&iuml;ve "GET tokens, compute refill, if enough SET tokens" from Go
is three round trips with a race between them: two gateway instances
(or even two goroutines in one instance) could both read the same
"tokens=1", both decide to allow, and both write "tokens=0" - the limit
just got violated by 2x. A single Lua script executes atomically inside
Redis (Redis never interleaves two scripts), so the whole read-refill-
check-write happens as one indivisible step regardless of how many
gateway processes call it concurrently.

### 18. What does "the limit is shared, not per-instance" actually mean operationally, and how is it tested?

Two gatekeeper processes behind a load balancer, both pointed at the
same Redis, should together honor one rate limit for a given key - not
each independently allow `burst` requests (which would let a client
effectively double their limit by hitting both instances).
`TestRedis_SharedAcrossTwoLimiterInstances` (`internal/ratelimit/
redis_test.go`) and its gateway-level equivalent
(`TestGateway_RateLimit_RedisBackendSharedAcrossTwoGateways`,
integration-tagged) construct two independent `*ratelimit.Redis`/
`*gateway.Gateway` values against the same Redis and same key, and
assert that the *combined* request count across both respects the
configured burst - proving the sharing, not just that each one works
in isolation.

### 19. Why does the local (non-Redis) rate limiter shard its map, and what would go wrong without it?

`Local.Allow` (`internal/ratelimit/local.go`) hashes the key to pick
one of 16 shards, each with its own `sync.Mutex`-guarded map. Without
sharding, every rate-limited request - regardless of key - would
contend on one global lock just to look up its own bucket, even though
different keys' buckets are logically independent. Sharding spreads
that contention across 16 locks instead of one, while each individual
bucket's refill math still only needs its own per-bucket mutex (a
separate, finer-grained lock than the shard's map lock).

## Design/architecture

### 20. Why is `gateway.Snapshot` immutable, with `atomic.Pointer` swap, rather than a mutable struct behind a `sync.RWMutex`?

An `RWMutex`-guarded mutable struct means every request takes a read
lock for the *whole* duration it's using the config (route lookup,
balancer pick, everything) - a reload (write lock) would have to wait
for all of that to drain, and a long-held read lock during a slow
upstream call would block the reload indefinitely. With an immutable
`*Snapshot` behind `atomic.Pointer`, a request loads the pointer once
(a single atomic read, not held for the request's duration) and uses
that specific `*Snapshot` value for as long as it needs, however long
that is; `Swap` is a single atomic store that doesn't wait for
anything. Readers and the writer never block each other.

### 21. Why does `proxyHandler` buffer the response into memory only when retries are actually possible, rather than always?

Buffering is what makes discarding a failed attempt possible - once
bytes are written to the real `http.ResponseWriter`, they can't be
un-sent, so a retry has to happen *before* that commitment. But
buffering costs an allocation and copy proportional to response size,
and breaks streaming (the whole body has to arrive before any of it
can be forwarded) - real costs that most requests (non-idempotent
methods, or routes with `retries.max: 0`) shouldn't pay for a
capability they don't use. `proxyOnce` is the no-retry fast path: a
thin status-recording wrapper with zero buffering, functionally
identical to how the proxy worked before retries existed at all.

### 22. The middleware chain order is `recover -> request-id -> access-log -> metrics/tracing -> CORS -> body-limit -> route-match -> auth -> rate-limit -> circuit-breaker -> proxy`. Justify two of these orderings.

`recover` first (outermost) because it has to catch a panic from
*every* other layer, including the middleware chain itself - a panic
inside, say, the CORS or auth logic still needs to become a 500, not a
crashed process. `auth` before `rate-limit`: an unauthenticated request
should be rejected before spending a token bucket entry on it - rate
limiting an attacker's invalid-credential requests just means the
*next legitimate* request from the same key/IP might get throttled by
someone else's junk traffic.

### 23. What's the actual difference between `/healthz` and `/readyz` in this project, and why does the admin API expose both unauthenticated while the rest of `/admin/*` requires a Bearer token?

`/healthz` is unconditional liveness (process is up, always `200` once
serving) - what an orchestrator uses to decide "should I restart this
container." `/readyz` reflects whether `Gateway.Current()` has loaded a
snapshot yet (`200` once it has, `503` in the brief startup window
before the first `Build`/`Swap`) - what a load balancer uses to decide
"should I send this instance traffic yet." Both, plus `/metrics`, skip
the Bearer check deliberately: orchestrator probes and Prometheus
scrapes conventionally hit these without credentials, and none of the
three leaks anything sensitive (routes/upstreams/reload, which *can*
reveal internal topology or mutate config, do require the token).

### 24. Why does `config.Load` reject unknown YAML fields instead of silently ignoring them?

`yaml.Decoder.KnownFields(true)` turns a typo (`tiemout: 30s` instead
of `timeout: 30s`) into a hard startup error instead of the field
silently taking its zero value - which for something like a timeout
would mean "instant timeout" or "no timeout," either of which is a
much worse failure mode discovered in production than a config file
that simply refuses to parse.

### 25. Why do metrics like `gateway_upstream_healthy` and `gateway_circuit_state` get updated on a timer (`Gateway.RefreshMetrics`, polled every 5s) instead of exactly when they change?

The other metrics (`gateway_requests_total`, `..._retries_total`, etc.)
have a natural triggering event - a request happened, a retry
happened - so recording them exactly when that event occurs is
correct. Health and breaker state are *ongoing conditions*, not
events: a target can sit unhealthy for minutes with zero requests
touching it, and there'd be no request-shaped moment to update its
gauge from. Polling on a timer is the straightforward way to keep an
ongoing-condition gauge current independent of traffic.
