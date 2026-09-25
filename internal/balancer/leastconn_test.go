package balancer

import (
	"sync"
	"testing"
)

func TestLeastConn_PicksFewestActiveConns(t *testing.T) {
	targets := mustTargets(t, "http://a", "http://b")
	b := NewLeastConn(targets)

	// Both start at 0 active conns; ties go to the first-declared target.
	first, release, err := b.Pick()
	if err != nil {
		t.Fatalf("Pick() #1: %v", err)
	}
	if release == nil {
		t.Fatal("Pick() returned a nil release func")
	}
	if first.URL.Host != "a" {
		t.Fatalf("Pick() #1 = %q, want %q (tie-break to first-declared)", first.URL.Host, "a")
	}

	// "a" is not released, so it now has one more active conn than "b" -
	// the next pick must go to "b".
	second, _, err := b.Pick()
	if err != nil {
		t.Fatalf("Pick() #2: %v", err)
	}
	if second.URL.Host != "b" {
		t.Errorf("Pick() #2 = %q, want %q (a has more active conns)", second.URL.Host, "b")
	}
}

func TestLeastConn_ReleaseDecrementsCount(t *testing.T) {
	targets := mustTargets(t, "http://a")
	b := NewLeastConn(targets)

	_, release, err := b.Pick()
	if err != nil {
		t.Fatalf("Pick(): %v", err)
	}
	if got := targets[0].ActiveConns(); got != 1 {
		t.Fatalf("ActiveConns after Pick = %d, want 1", got)
	}
	release()
	if got := targets[0].ActiveConns(); got != 0 {
		t.Fatalf("ActiveConns after release = %d, want 0", got)
	}
}

func TestLeastConn_SkipsUnhealthyTarget(t *testing.T) {
	targets := mustTargets(t, "http://a", "http://b")
	targets[0].SetHealthy(false)

	b := NewLeastConn(targets)
	got, _, err := b.Pick()
	if err != nil {
		t.Fatalf("Pick(): %v", err)
	}
	if got.URL.Host != "b" {
		t.Errorf("Pick() = %q, want %q (a is unhealthy)", got.URL.Host, "b")
	}
}

func TestLeastConn_AllUnhealthyReturnsErrNoTargets(t *testing.T) {
	targets := mustTargets(t, "http://a")
	targets[0].SetHealthy(false)

	b := NewLeastConn(targets)
	if _, _, err := b.Pick(); err != ErrNoTargets {
		t.Fatalf("Pick() error = %v, want %v", err, ErrNoTargets)
	}
}

func TestLeastConn_Targets(t *testing.T) {
	targets := mustTargets(t, "http://a", "http://b")
	b := NewLeastConn(targets)
	if got := b.Targets(); len(got) != 2 {
		t.Errorf("Targets() = %v, want 2 entries", got)
	}
}

func TestLeastConn_ConcurrentPickRelease(t *testing.T) {
	targets := mustTargets(t, "http://a", "http://b", "http://c")
	b := NewLeastConn(targets)

	var wg sync.WaitGroup
	for g := 0; g < 20; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				_, release, err := b.Pick()
				if err != nil {
					t.Errorf("Pick(): unexpected error: %v", err)
					return
				}
				release()
			}
		}()
	}
	wg.Wait()

	for _, tgt := range targets {
		if got := tgt.ActiveConns(); got != 0 {
			t.Errorf("target %s ActiveConns = %d, want 0 after all releases", tgt.URL.Host, got)
		}
	}
}
