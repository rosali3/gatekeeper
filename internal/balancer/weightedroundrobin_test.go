package balancer

import (
	"net/url"
	"testing"
)

func mustWeightedTargets(t *testing.T, weights map[string]int, order ...string) []*Target {
	t.Helper()
	targets := make([]*Target, len(order))
	for i, name := range order {
		u, err := url.Parse("http://" + name)
		if err != nil {
			t.Fatalf("url.Parse: %v", err)
		}
		targets[i] = newTarget(u, weights[name])
	}
	return targets
}

func pickSequence(t *testing.T, b Balancer, n int) []string {
	t.Helper()
	seq := make([]string, n)
	for i := 0; i < n; i++ {
		target, release, err := b.Pick()
		if err != nil {
			t.Fatalf("Pick() #%d: unexpected error: %v", i, err)
		}
		release()
		seq[i] = target.URL.Host
	}
	return seq
}

// TestWeightedRoundRobin_ClassicNginxSequence checks the exact,
// deterministic pick order for nginx's textbook smooth-WRR example
// (weights 5:1:1), not just an approximate ratio - the algorithm's whole
// point is smoothing bursts, so the precise sequence is the real spec.
func TestWeightedRoundRobin_ClassicNginxSequence(t *testing.T) {
	targets := mustWeightedTargets(t, map[string]int{"a": 5, "b": 1, "c": 1}, "a", "b", "c")
	b := NewWeightedRoundRobin(targets)

	got := pickSequence(t, b, 7)
	want := []string{"a", "a", "b", "a", "c", "a", "a"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pick sequence = %v, want %v", got, want)
		}
	}
}

func TestWeightedRoundRobin_SequenceRepeatsEveryTotalWeightPicks(t *testing.T) {
	targets := mustWeightedTargets(t, map[string]int{"a": 5, "b": 1, "c": 1}, "a", "b", "c")
	b := NewWeightedRoundRobin(targets)

	first := pickSequence(t, b, 7)
	second := pickSequence(t, b, 7)
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("period 2 = %v, want it to repeat period 1 = %v", second, first)
			break
		}
	}
}

func TestWeightedRoundRobin_SkipsUnhealthyTarget(t *testing.T) {
	targets := mustWeightedTargets(t, map[string]int{"a": 1, "b": 1}, "a", "b")
	targets[1].SetHealthy(false)

	b := NewWeightedRoundRobin(targets)
	for i := 0; i < 4; i++ {
		target, _, err := b.Pick()
		if err != nil {
			t.Fatalf("Pick() #%d: unexpected error: %v", i, err)
		}
		if target.URL.Host != "a" {
			t.Errorf("Pick() #%d = %q, want %q (b is unhealthy)", i, target.URL.Host, "a")
		}
	}
}

func TestWeightedRoundRobin_AllUnhealthyReturnsErrNoTargets(t *testing.T) {
	targets := mustWeightedTargets(t, map[string]int{"a": 1}, "a")
	targets[0].SetHealthy(false)

	b := NewWeightedRoundRobin(targets)
	if _, _, err := b.Pick(); err != ErrNoTargets {
		t.Fatalf("Pick() error = %v, want %v", err, ErrNoTargets)
	}
}

func TestWeightedRoundRobin_Targets(t *testing.T) {
	targets := mustWeightedTargets(t, map[string]int{"a": 1, "b": 1}, "a", "b")
	b := NewWeightedRoundRobin(targets)
	if got := b.Targets(); len(got) != 2 {
		t.Errorf("Targets() = %v, want 2 entries", got)
	}
}
