package balancer

import "sync"

// weightedEntry tracks one target's running "current weight" for the
// smooth weighted round-robin algorithm.
type weightedEntry struct {
	target        *Target
	currentWeight int
}

// WeightedRoundRobin implements nginx's smooth weighted round-robin: each
// pick adds every healthy target's weight to its running current weight,
// selects the target with the highest current weight, then subtracts the
// sum of healthy weights from the winner. Unlike plain round-robin this
// can't be done with a single atomic counter - selecting the max requires
// seeing every entry's updated weight at once - so it's guarded by a
// mutex. That's fine: WeightedRoundRobin.Pick does a handful of integer
// ops under the lock, nowhere near expensive enough to need a lock-free
// scheme.
type WeightedRoundRobin struct {
	mu      sync.Mutex
	entries []*weightedEntry
}

func NewWeightedRoundRobin(targets []*Target) *WeightedRoundRobin {
	entries := make([]*weightedEntry, len(targets))
	for i, t := range targets {
		entries[i] = &weightedEntry{target: t}
	}
	return &WeightedRoundRobin{entries: entries}
}

func (b *WeightedRoundRobin) Targets() []*Target {
	targets := make([]*Target, len(b.entries))
	for i, e := range b.entries {
		targets[i] = e.target
	}
	return targets
}

func (b *WeightedRoundRobin) Pick() (*Target, func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	var selected *weightedEntry
	healthyWeight := 0
	for _, e := range b.entries {
		if !e.target.Healthy() {
			continue
		}
		e.currentWeight += e.target.Weight
		healthyWeight += e.target.Weight
		if selected == nil || e.currentWeight > selected.currentWeight {
			selected = e
		}
	}
	if selected == nil {
		return nil, noop, ErrNoTargets
	}
	selected.currentWeight -= healthyWeight
	return selected.target, noop, nil
}
