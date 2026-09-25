package balancer

import (
	"net/url"
	"sync"
	"testing"
)

func mustTargets(t *testing.T, raw ...string) []*Target {
	t.Helper()
	targets := make([]*Target, len(raw))
	for i, r := range raw {
		u, err := url.Parse(r)
		if err != nil {
			t.Fatalf("url.Parse(%q): %v", r, err)
		}
		targets[i] = newTarget(u, 1)
	}
	return targets
}

func TestRoundRobin_NoTargets(t *testing.T) {
	b := NewRoundRobin(nil)
	if _, _, err := b.Pick(); err != ErrNoTargets {
		t.Fatalf("Pick() error = %v, want %v", err, ErrNoTargets)
	}
}

func TestRoundRobin_CyclesInOrder(t *testing.T) {
	targets := mustTargets(t, "http://a", "http://b", "http://c")
	b := NewRoundRobin(targets)

	want := []string{"http://a", "http://b", "http://c", "http://a", "http://b"}
	for i, w := range want {
		got, release, err := b.Pick()
		if err != nil {
			t.Fatalf("Pick() #%d: unexpected error: %v", i, err)
		}
		release()
		if got.URL.String() != w {
			t.Errorf("Pick() #%d = %q, want %q", i, got.URL.String(), w)
		}
	}
}

func TestRoundRobin_SkipsUnhealthyTargets(t *testing.T) {
	targets := mustTargets(t, "http://a", "http://b", "http://c")
	targets[1].SetHealthy(false) // b is down

	b := NewRoundRobin(targets)
	for i := 0; i < 6; i++ {
		got, _, err := b.Pick()
		if err != nil {
			t.Fatalf("Pick() #%d: unexpected error: %v", i, err)
		}
		if got.URL.String() == "http://b" {
			t.Errorf("Pick() #%d returned the unhealthy target", i)
		}
	}
}

func TestRoundRobin_AllUnhealthyReturnsErrNoTargets(t *testing.T) {
	targets := mustTargets(t, "http://a", "http://b")
	for _, tgt := range targets {
		tgt.SetHealthy(false)
	}

	b := NewRoundRobin(targets)
	if _, _, err := b.Pick(); err != ErrNoTargets {
		t.Fatalf("Pick() error = %v, want %v", err, ErrNoTargets)
	}
}

func TestRoundRobin_Targets(t *testing.T) {
	targets := mustTargets(t, "http://a", "http://b")
	b := NewRoundRobin(targets)
	if got := b.Targets(); len(got) != 2 {
		t.Errorf("Targets() = %v, want 2 entries", got)
	}
}

func TestRoundRobin_EvenDistributionUnderConcurrency(t *testing.T) {
	targets := mustTargets(t, "http://a", "http://b", "http://c", "http://d")
	b := NewRoundRobin(targets)

	const perGoroutine = 1000
	const goroutines = 20
	counts := make([]int, len(targets))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := make([]int, len(targets))
			for i := 0; i < perGoroutine; i++ {
				target, release, err := b.Pick()
				if err != nil {
					t.Errorf("Pick(): unexpected error: %v", err)
					return
				}
				release()
				for idx, tgt := range targets {
					if tgt == target {
						local[idx]++
						break
					}
				}
			}
			mu.Lock()
			for i, c := range local {
				counts[i] += c
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	total := goroutines * perGoroutine
	want := total / len(targets)
	for i, c := range counts {
		if c != want {
			t.Errorf("target %d got %d picks, want exactly %d (perfectly even since %d divides %d)", i, c, want, len(targets), total)
		}
	}
}
