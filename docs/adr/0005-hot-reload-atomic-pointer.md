# ADR 0005: Hot reload via atomic.Pointer[Snapshot], with per-resource reuse

## Status
Accepted

## Context
A config reload must not drop in-flight requests, must not reset
running state (health, breaker, rate-limit buckets) for parts of the
config that didn't change, and must not leak the goroutines (health
checkers, rate-limiter janitors) belonging to whatever *did* change or
got removed.

## Decision
`gateway.Gateway` holds `atomic.Pointer[Snapshot]`. Each `Snapshot` is
built once (`gateway.Build`) and never mutated after that - every
request loads the current pointer once at the top of `ServeHTTP` and
uses that same `*Snapshot` for its whole lifetime, so a `Swap` mid-
request never causes it to see a half-old, half-new picture. A request
that's already in flight when `Swap` runs keeps using the `*Snapshot`
it already loaded; the next request loads whatever's current.

Reuse is per-resource, not per-generation: `Build` takes the previous
`Snapshot` and, for each upstream/route whose relevant config is
`reflect.DeepEqual` to before, copies the *same* `*Upstream`/
`*routeRateLimit` pointer into the new `Snapshot` - same balancer (with
whatever target health/active-conn state it already has), same circuit
breaker (mid-trip state and all), same rate limiter (bucket contents
and all). Anything not reused gets a fresh instance with its own
`context.CancelFunc`, stored on the struct itself
(`Upstream.stopHealthChecker`, `routeRateLimit.stopJanitor`); `Build`
calls those for whatever existed in the previous `Snapshot` but wasn't
carried over, so a changed or removed upstream's checker goroutine
stops instead of accumulating across repeated reloads.

A single per-generation context (cancel everything belonging to
generation N when generation N+1 replaces it) was the first design
considered and rejected: it can't express "keep this specific
goroutine running across the swap," which is exactly what per-upstream
reuse needs.

## Consequences
- Routes are matched across reloads by index into `[]config.
  RouteConfig` - the schema has no stable id/name field for a route.
  Reordering routes in the config defeats reuse for the reordered ones
  (they're rebuilt fresh, harmlessly) rather than misattributing state
  to the wrong route.
- This is exactly what `TestReload_UnchangedUpstreamPreservesBreakerState`
  /`TestReload_ChangedUpstreamGetsFreshState` /
  `TestReload_RemovedUpstreamStopsCleanly` (goleak-checked) in
  `internal/gateway/reload_test.go` verify directly, rather than
  trusting the design by inspection.
- The reload trigger itself (SIGHUP, fsnotify on the config file's
  directory with a debounce, or `POST /admin/reload`) is orthogonal to
  this - all three just call the same `config.Load` + `gateway.Build` +
  `Gateway.Swap` sequence in `cmd/gatekeeper`.
