# ADR 0006: Retry budget as a fraction of request volume, not attempts-per-request

## Status
Accepted

## Context
`retries.max` alone caps how many times *one* request can be retried,
but says nothing about aggregate load: if an upstream is failing enough
that most requests exhaust their retries, a naive "always retry up to
max" policy multiplies traffic to an already-struggling target - the
classic retry storm.

## Decision
`RetryBudget` (`internal/breaker/budget.go`) tracks `(requests,
retries)` over a trailing window (the same bucketed-sliding-window
technique as ADR 0004's circuit breaker, applied to a different pair of
counters) and only approves a retry if `retries / requests <=
budget_ratio` *after* counting it. `RecordRequest` is called once per
*original* incoming request (regardless of method or whether it ends
up retried); `AllowRetry` is what a retry attempt checks before being
allowed to happen.

With zero requests recorded in the window, `AllowRetry` denies rather
than allows - there's no baseline to compute a ratio against, and
denying is the conservative direction for a mechanism whose entire
purpose is capping retry volume.

## Consequences
- The budget's denominator is *aggregate* request volume, not "this
  request's own attempt count" - a single isolated request in a test
  with no prior traffic will find its second retry denied even with
  `budget_ratio: 1.0`, because `(1 retry + 1) / 1 request > 1.0`. This
  is correct aggregate behavior, not a bug, but it means a test
  exercising `retries.max` specifically needs to seed some baseline
  request volume first (see `TestGateway_Retry_StopsAtMaxAttempts` in
  `internal/gateway/retry_test.go`) rather than expect `max` alone to
  guarantee that many retries in isolation.
- Retries only happen at all for idempotent methods (or any method, if
  `only_idempotent: false` opts out) and only for network errors/502/
  503/504 - a plain `500` from a reachable, responding upstream isn't
  retried, since retrying an application error rarely helps and isn't
  what the budget is meant to protect against.
- Because retrying means possibly sending the same request to a
  *different* target, the response can't be streamed straight through
  as it arrives - it has to be buffered per attempt and only flushed
  once an attempt is known to be final, so a failed attempt's partial
  output is never mixed into what the client sees. That buffering only
  happens on the retry-capable path (`retries.max > 0` and an eligible
  method); everything else streams through a thin status-recording
  wrapper exactly as before, at no extra allocation cost.
