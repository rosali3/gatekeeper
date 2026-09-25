package balancer

import "sync/atomic"

// RoundRobin cycles through targets using a lock-free atomic counter.
type RoundRobin struct {
	targets []*Target
	counter atomic.Uint64
}

func NewRoundRobin(targets []*Target) *RoundRobin {
	return &RoundRobin{targets: targets}
}

func (b *RoundRobin) Pick() (*Target, error) {
	n := len(b.targets)
	if n == 0 {
		return nil, ErrNoTargets
	}
	// Add returns the post-increment value, so subtract 1 to get a
	// zero-based, still-monotonic index.
	i := (b.counter.Add(1) - 1) % uint64(n)
	return b.targets[i], nil
}
