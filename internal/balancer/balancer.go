// Package balancer picks a backend target for an upstream. Algorithms are
// hand-written (no x/time/rate-style library) since implementing them
// correctly under concurrency is the point of this project.
package balancer

import (
	"errors"
	"fmt"
	"net/url"
)

// ErrNoTargets is returned when a balancer has no target to hand out.
// Today that only happens with a misconfigured (empty) target list; once
// health checks land, it also covers "every target is unhealthy".
var ErrNoTargets = errors.New("balancer: no targets available")

// Target is a single backend a balancer can route a request to.
type Target struct {
	URL *url.URL
}

// Balancer selects one target per request.
type Balancer interface {
	Pick() (*Target, error)
}

// TargetSpec is the plain-data input used to build targets, kept separate
// from config.Target so this package doesn't depend on internal/config.
type TargetSpec struct {
	URL    string
	Weight int
}

// New builds the balancer named by algorithm. weighted_round_robin and
// least_conn are recognized (config.Validate accepts them) but not
// implemented until the health-check stage, since least_conn needs
// per-target active-connection tracking and weighted_round_robin's
// smoothing only matters once unhealthy targets can be skipped.
func New(algorithm string, specs []TargetSpec) (Balancer, error) {
	targets := make([]*Target, 0, len(specs))
	for _, s := range specs {
		u, err := url.Parse(s.URL)
		if err != nil {
			return nil, fmt.Errorf("balancer: invalid target URL %q: %w", s.URL, err)
		}
		targets = append(targets, &Target{URL: u})
	}

	switch algorithm {
	case "round_robin":
		return NewRoundRobin(targets), nil
	case "weighted_round_robin", "least_conn":
		return nil, fmt.Errorf("balancer %q: not implemented yet", algorithm)
	default:
		return nil, fmt.Errorf("balancer %q: unknown", algorithm)
	}
}
