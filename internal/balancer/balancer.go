// Package balancer picks a backend target for an upstream. Algorithms are
// hand-written (no x/time/rate-style library) since implementing them
// correctly under concurrency is the point of this project.
package balancer

import (
	"errors"
	"fmt"
	"net/url"
	"sync/atomic"
)

// ErrNoTargets is returned when a balancer has no healthy target to hand
// out: either the target list is empty (a config bug, since
// config.Validate requires at least one) or every target is unhealthy.
var ErrNoTargets = errors.New("balancer: no healthy targets available")

// Target is a single backend a balancer can route a request to.
//
// Healthy and active-connection count are read on every single request
// (the hot path), so they're plain atomics rather than mutex-guarded -
// there's no multi-field invariant between them that needs a lock. The
// health-check package's threshold bookkeeping (consecutive
// success/failure counts), which only runs on health-check ticks or
// proxy errors, is a colder path and uses a mutex instead; see
// internal/health.Tracker.
type Target struct {
	URL    *url.URL
	Weight int

	healthy     atomic.Bool
	activeConns atomic.Int64
}

func newTarget(u *url.URL, weight int) *Target {
	t := &Target{URL: u, Weight: weight}
	t.healthy.Store(true) // assume healthy until a check says otherwise
	return t
}

// Healthy reports whether this target should currently receive traffic.
func (t *Target) Healthy() bool { return t.healthy.Load() }

// SetHealthy is called by the health-check package once its thresholds
// decide a target's status has changed.
func (t *Target) SetHealthy(v bool) { t.healthy.Store(v) }

// ActiveConns is the number of requests LeastConn currently has in flight
// for this target. Exposed for the admin API (stage 7).
func (t *Target) ActiveConns() int64 { return t.activeConns.Load() }

// Balancer selects one target per request. The returned release func must
// be called (typically via defer) once that request has finished; for
// algorithms with no per-request state to release it's a no-op.
type Balancer interface {
	Pick() (*Target, func(), error)
	Targets() []*Target
}

// noop is shared by balancers that don't need a real release callback, to
// avoid allocating a closure on every Pick.
func noop() {}

// TargetSpec is the plain-data input used to build targets, kept separate
// from config.Target so this package doesn't depend on internal/config.
type TargetSpec struct {
	URL    string
	Weight int
}

// New builds the balancer named by algorithm.
func New(algorithm string, specs []TargetSpec) (Balancer, error) {
	targets := make([]*Target, 0, len(specs))
	for _, s := range specs {
		u, err := url.Parse(s.URL)
		if err != nil {
			return nil, fmt.Errorf("balancer: invalid target URL %q: %w", s.URL, err)
		}
		targets = append(targets, newTarget(u, s.Weight))
	}

	switch algorithm {
	case "round_robin":
		return NewRoundRobin(targets), nil
	case "weighted_round_robin":
		return NewWeightedRoundRobin(targets), nil
	case "least_conn":
		return NewLeastConn(targets), nil
	default:
		return nil, fmt.Errorf("balancer %q: unknown", algorithm)
	}
}
