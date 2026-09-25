// Package health tracks each upstream target's health: active checks (a
// goroutine per upstream polling a health endpoint) and passive checks
// (the gateway reporting proxy errors it observed).
package health

import (
	"sync"

	"gatekeeper/internal/balancer"
)

// Tracker applies healthy/unhealthy consecutive-count thresholds for one
// target and flips its balancer.Target.Healthy flag when a threshold is
// crossed.
//
// Unlike Target's Healthy/ActiveConns fields, this does its bookkeeping
// under a mutex rather than atomics: reporting a result touches three
// related fields (both counters plus the healthy flag) that must move
// together, and this is only ever called on a health-check tick or a
// proxy error - nowhere near hot enough to need a lock-free scheme.
type Tracker struct {
	mu sync.Mutex

	target             *balancer.Target
	consecutiveSuccess int
	consecutiveFailure int
	healthyThreshold   int
	unhealthyThreshold int
}

func NewTracker(target *balancer.Target, healthyThreshold, unhealthyThreshold int) *Tracker {
	return &Tracker{
		target:             target,
		healthyThreshold:   healthyThreshold,
		unhealthyThreshold: unhealthyThreshold,
	}
}

// ReportSuccess records a successful check and returns true if this
// success just brought the target from unhealthy back to healthy.
func (t *Tracker) ReportSuccess() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.consecutiveFailure = 0
	t.consecutiveSuccess++
	if t.consecutiveSuccess >= t.healthyThreshold && !t.target.Healthy() {
		t.target.SetHealthy(true)
		return true
	}
	return false
}

// ReportFailure records a failed check and returns true if this failure
// just brought the target from healthy to unhealthy.
func (t *Tracker) ReportFailure() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.consecutiveSuccess = 0
	t.consecutiveFailure++
	if t.consecutiveFailure >= t.unhealthyThreshold && t.target.Healthy() {
		t.target.SetHealthy(false)
		return true
	}
	return false
}
