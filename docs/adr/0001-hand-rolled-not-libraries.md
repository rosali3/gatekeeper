# ADR 0001: Hand-rolled balancer/limiter/breaker, not libraries

## Status
Accepted

## Context
Go has mature libraries for every mechanism this gateway needs:
`golang.org/x/time/rate` for rate limiting, `sony/gobreaker` for circuit
breaking, any number of load-balancer packages. Pulling them in would
cut development time significantly.

## Decision
Implement round-robin/weighted-round-robin/least-conn balancing, the
local and Redis rate limiters, the circuit breaker, health checking and
hot reload entirely by hand, using only the standard library (`sync`,
`atomic`, `context`, `time`) plus the explicitly-allowed
yaml/jwt/redis/prometheus/otel/fsnotify libraries for things that
aren't the point of the exercise (parsing config, talking to Redis,
exposing metrics).

## Consequences
- This is a portfolio project aimed at a middle-Go interview bar. The
  value isn't "a working gateway" (that's `nginx.conf`) - it's being
  able to explain, line by line, why `RoundRobin.Pick` uses an atomic
  counter and not a mutex, why `CircuitBreaker`'s window is bucketed
  instead of storing one entry per request, why `LeastConn`'s release
  closure is the one place in the balancer package that allocates.
  A library would have hidden all of that.
- It also means bugs are our own. The retry-budget-vs-generation-
  counter mistake in the JWKS client (see the git history around
  `internal/auth/jwks.go`) is exactly the kind of concurrency bug a
  library would have already found and fixed - here, it had to be
  caught by a test.
- Anything genuinely orthogonal to "implement the mechanism correctly
  under concurrency" - YAML parsing, JWT signature verification, the
  Redis wire protocol, Prometheus's text exposition format, OTLP - is
  exactly what's excluded from this rule and pulled from a library.
  Reimplementing those wouldn't teach anything the rest of this project
  doesn't already cover.
