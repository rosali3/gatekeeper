package balancer

import "sync/atomic"

// RoundRobin cycles through healthy targets using a lock-free atomic
// counter.
type RoundRobin struct {
	targets []*Target
	counter atomic.Uint64
}

func NewRoundRobin(targets []*Target) *RoundRobin {
	return &RoundRobin{targets: targets}
}

func (b *RoundRobin) Targets() []*Target { return b.targets }

// Pick advances the counter once, then scans forward at most len(targets)
// steps for the first healthy target - so an unhealthy target is skipped
// without disturbing the round-robin order of the healthy ones.
func (b *RoundRobin) Pick() (*Target, func(), error) {
	n := len(b.targets)
	if n == 0 {
		return nil, noop, ErrNoTargets
	}
	// Add returns the post-increment value, so subtract 1 to get a
	// zero-based, still-monotonic starting index.
	start := b.counter.Add(1) - 1
	for i := uint64(0); i < uint64(n); i++ {
		t := b.targets[(start+i)%uint64(n)]
		if t.Healthy() {
			return t, noop, nil
		}
	}
	return nil, noop, ErrNoTargets
}
