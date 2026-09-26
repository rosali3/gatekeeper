// Package breaker implements a per-upstream circuit breaker
// (closed → open → half-open) and a retry budget, both using the same
// bucketed sliding-window technique to count events over a trailing
// window without storing one entry per request.
package breaker

import "time"

// Clock is time as seen by a breaker/budget. Injected so tests can
// advance time deterministically instead of calling time.Sleep, same
// rationale as internal/ratelimit.Clock (kept as a separate copy rather
// than a shared package - it's a 3-line interface, not worth a layer).
type Clock interface {
	Now() time.Time
}

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

// numBuckets is the sliding window's granularity: the window is split
// into this many equal slices, each independently aged out once it falls
// outside the window. Fixed rather than config-exposed, like several
// other internal tuning constants in this project.
const numBuckets = 10

// bucketIndex returns which of numBuckets slots a moment in time falls
// into, and that slot's own window-start timestamp (unix nanos) - used to
// detect a stale slot being reused after a full cycle. bucketDuration is
// window/numBuckets.
func bucketIndex(now time.Time, bucketDuration time.Duration) (idx int, windowStart int64) {
	bd := bucketDuration.Nanoseconds()
	if bd <= 0 {
		bd = 1
	}
	slot := now.UnixNano() / bd
	return int(slot % int64(numBuckets)), slot * bd
}

// bucketValid reports whether a stored bucket (written at windowStart) is
// still within the trailing window as of now - i.e. whether it should
// still count toward the running sum, without needing to physically clear
// buckets on every tick.
func bucketValid(now time.Time, windowStart int64, window time.Duration) bool {
	if windowStart == 0 {
		return false
	}
	return now.UnixNano()-windowStart <= window.Nanoseconds()
}
