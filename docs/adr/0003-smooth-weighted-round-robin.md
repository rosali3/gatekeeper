# ADR 0003: Smooth weighted round robin (nginx's algorithm), not naive weighted round robin

## Status
Accepted

## Context
"Weighted round robin" has two common implementations. The naive one
expands each target into `weight` consecutive slots (weights 5:1:1 ->
sequence `A A A A A B C`, repeated) - simple, but bursty: target A gets
five requests in a row before B or C see any. The smooth version
(popularized by nginx, and by Galil/Naor's earlier "smooth spread"
literature) interleaves them: `A A B A C A A` for the same 5:1:1
weights - same 5:1:1 ratio over a full cycle, but no run of more than
two consecutive picks to the same target.

## Decision
Implement smooth weighted round robin (`internal/balancer/
weighted_round_robin.go`): each target tracks a running `currentWeight`;
every `Pick()` adds every healthy target's `weight` to its
`currentWeight`, selects the target with the highest resulting value,
then subtracts the sum of healthy weights from the winner. This
converges to the same long-run ratio as the naive version while
smoothing the distribution within each cycle.

`WeightedRoundRobin.Pick` holds `entries` under a plain `sync.Mutex`,
unlike `RoundRobin`'s lock-free atomic counter: selecting the "highest
currentWeight" target requires reading and updating every entry
together as one step, which an atomic-per-field scheme can't do without
introducing its own race (two goroutines could both read stale
`currentWeight`s and pick the same "winner", or update in an order that
doesn't converge to the right ratio). The lock only guards a handful of
integer additions/comparisons - nowhere near hot enough (unlike, say, a
single `Target.Healthy()` read on every request) to justify a lock-free
scheme.

## Consequences
- Correctness isn't just "roughly proportional over many picks" - it's
  the *exact* sequence. `TestWeightedRoundRobin_ClassicNginxSequence`
  asserts the literal 5:1:1 -> `a a b a c a a` sequence byte for byte,
  not an approximate ratio over a large sample, because that sequence
  *is* the algorithm's specification, not an emergent property of it.
- Unlike `RoundRobin`, a target excluded from a pick because it's
  unhealthy doesn't have its `currentWeight` touched that round; when it
  recovers, it resumes from wherever it was, rather than needing to
  "catch up" or being reset to zero.
