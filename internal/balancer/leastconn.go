package balancer

// LeastConn picks the healthy target with the fewest requests currently in
// flight, tracked via each Target's atomic active-connection counter.
type LeastConn struct {
	targets []*Target
}

func NewLeastConn(targets []*Target) *LeastConn {
	return &LeastConn{targets: targets}
}

func (b *LeastConn) Targets() []*Target { return b.targets }

func (b *LeastConn) Pick() (*Target, func(), error) {
	var selected *Target
	var min int64
	for _, t := range b.targets {
		if !t.Healthy() {
			continue
		}
		c := t.activeConns.Load()
		if selected == nil || c < min {
			selected, min = t, c
		}
	}
	if selected == nil {
		return nil, noop, ErrNoTargets
	}
	selected.activeConns.Add(1)
	return selected, func() { selected.activeConns.Add(-1) }, nil
}
