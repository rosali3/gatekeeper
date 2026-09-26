package breaker

import (
	"sync"
	"time"
)

// State is one of the three circuit breaker states.
type State int

const (
	StateClosed State = iota
	StateOpen
	StateHalfOpen
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}

type resultBucket struct {
	windowStart int64
	successes   int
	failures    int
}

// CircuitBreaker protects one upstream: once failures cross
// failureRatio (with at least minRequests samples in the window), it
// trips to open and every Allow() call is rejected without touching the
// upstream until openTimeout passes, then a limited number of half-open
// probes decide whether to close again or reopen.
type CircuitBreaker struct {
	mu sync.Mutex

	clock          Clock
	failureRatio   float64
	minRequests    int
	window         time.Duration
	bucketDuration time.Duration
	openTimeout    time.Duration
	halfOpenMax    int

	state   State
	buckets [numBuckets]resultBucket

	openedAt time.Time

	halfOpenDispatched int
	halfOpenSuccesses  int
}

type Config struct {
	FailureRatio float64
	MinRequests  int
	Window       time.Duration
	OpenTimeout  time.Duration
	HalfOpenMax  int
}

func New(clock Clock, cfg Config) *CircuitBreaker {
	return &CircuitBreaker{
		clock:          clock,
		failureRatio:   cfg.FailureRatio,
		minRequests:    cfg.MinRequests,
		window:         cfg.Window,
		bucketDuration: cfg.Window / numBuckets,
		openTimeout:    cfg.OpenTimeout,
		halfOpenMax:    cfg.HalfOpenMax,
	}
}

// Allow reports whether a request should be attempted at all. In the
// open state this is what makes a tripped breaker return 503 without
// ever dialing the upstream - checked before proxying, not after.
func (b *CircuitBreaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateClosed:
		return true
	case StateOpen:
		if b.clock.Now().Sub(b.openedAt) < b.openTimeout {
			return false
		}
		b.state = StateHalfOpen
		b.halfOpenDispatched = 0
		b.halfOpenSuccesses = 0
		b.halfOpenDispatched++
		return true
	case StateHalfOpen:
		if b.halfOpenDispatched >= b.halfOpenMax {
			return false
		}
		b.halfOpenDispatched++
		return true
	default:
		return true
	}
}

// RecordResult reports the outcome of a request that Allow let through.
func (b *CircuitBreaker) RecordResult(success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock.Now()

	if b.state == StateHalfOpen {
		if !success {
			b.state = StateOpen
			b.openedAt = now
			return
		}
		b.halfOpenSuccesses++
		if b.halfOpenSuccesses >= b.halfOpenMax {
			b.state = StateClosed
			b.buckets = [numBuckets]resultBucket{}
		}
		return
	}

	// Closed, or a straggler result from a request dispatched before the
	// breaker tripped to open (Allow already rejects new attempts while
	// open, but one already in flight can still complete). Either way,
	// feed the sliding window and re-check the trip condition only if
	// we're still closed.
	b.record(now, success)
	if b.state == StateClosed {
		successes, failures := b.counts(now)
		total := successes + failures
		if total >= b.minRequests && float64(failures)/float64(total) >= b.failureRatio {
			b.state = StateOpen
			b.openedAt = now
		}
	}
}

// State reports the current state, e.g. for the admin API/metrics.
func (b *CircuitBreaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

func (b *CircuitBreaker) record(now time.Time, success bool) {
	idx, ws := bucketIndex(now, b.bucketDuration)
	if b.buckets[idx].windowStart != ws {
		b.buckets[idx] = resultBucket{windowStart: ws}
	}
	if success {
		b.buckets[idx].successes++
	} else {
		b.buckets[idx].failures++
	}
}

func (b *CircuitBreaker) counts(now time.Time) (successes, failures int) {
	for _, bucket := range b.buckets {
		if bucketValid(now, bucket.windowStart, b.window) {
			successes += bucket.successes
			failures += bucket.failures
		}
	}
	return successes, failures
}
