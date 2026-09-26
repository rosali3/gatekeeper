package breaker

import (
	"sync"
	"time"
)

// budgetWindow is the retry budget's trailing window. Not config-exposed
// (retries.budget_ratio has no accompanying window field in the schema),
// so it's a fixed default like several other internal tuning constants.
const budgetWindow = 10 * time.Second

type budgetBucket struct {
	windowStart int64
	requests    int
	retries     int
}

// RetryBudget caps retries at ratio of the recent original-request
// volume for one upstream (e.g. ratio=0.1 allows roughly one retry per
// ten requests), so a struggling upstream can't be hit with a multiplying
// storm of retries on top of its already-elevated failure rate.
type RetryBudget struct {
	mu             sync.Mutex
	clock          Clock
	ratio          float64
	bucketDuration time.Duration
	buckets        [numBuckets]budgetBucket
}

func NewRetryBudget(clock Clock, ratio float64) *RetryBudget {
	return &RetryBudget{
		clock:          clock,
		ratio:          ratio,
		bucketDuration: budgetWindow / numBuckets,
	}
}

// RecordRequest counts one original (non-retry) request toward the
// budget's denominator. Call this once per incoming request, regardless
// of whether it ends up being retried.
func (b *RetryBudget) RecordRequest() {
	b.mu.Lock()
	defer b.mu.Unlock()
	idx, ws := bucketIndex(b.clock.Now(), b.bucketDuration)
	if b.buckets[idx].windowStart != ws {
		b.buckets[idx] = budgetBucket{windowStart: ws}
	}
	b.buckets[idx].requests++
}

// AllowRetry reports whether one more retry fits within the budget, and
// if so, counts it. There's no minRequests floor here (unlike the
// breaker): with zero recorded requests the ratio is undefined, and
// denying by default is the safe choice - it can only make an already-
// pathological situation (retrying before a single real request has even
// been recorded) more conservative, never less.
func (b *RetryBudget) AllowRetry() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock.Now()

	var requests, retries int
	for _, bucket := range b.buckets {
		if bucketValid(now, bucket.windowStart, budgetWindow) {
			requests += bucket.requests
			retries += bucket.retries
		}
	}
	if requests == 0 || float64(retries+1)/float64(requests) > b.ratio {
		return false
	}

	idx, ws := bucketIndex(now, b.bucketDuration)
	if b.buckets[idx].windowStart != ws {
		b.buckets[idx] = budgetBucket{windowStart: ws}
	}
	b.buckets[idx].retries++
	return true
}
