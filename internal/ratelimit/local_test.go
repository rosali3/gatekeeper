package ratelimit

import (
	"context"
	"sync"
	"testing"
	"time"
)

var epoch = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

func TestLocal_AllowsUpToBurstThenDenies(t *testing.T) {
	clock := newFakeClock(epoch)
	l := NewLocal(clock, Config{RPS: 1, Burst: 3})

	for i := 0; i < 3; i++ {
		res, err := l.Allow(context.Background(), "k")
		if err != nil {
			t.Fatalf("Allow() #%d: unexpected error: %v", i, err)
		}
		if !res.Allowed {
			t.Fatalf("Allow() #%d: denied, want allowed (within burst)", i)
		}
	}

	res, err := l.Allow(context.Background(), "k")
	if err != nil {
		t.Fatalf("Allow() #4: unexpected error: %v", err)
	}
	if res.Allowed {
		t.Fatal("Allow() #4: allowed, want denied (burst exhausted)")
	}
	if res.RetryAfter <= 0 {
		t.Errorf("RetryAfter = %v, want > 0 when denied", res.RetryAfter)
	}
}

func TestLocal_RefillsOverTimeWithoutSleeping(t *testing.T) {
	clock := newFakeClock(epoch)
	l := NewLocal(clock, Config{RPS: 1, Burst: 1})

	if res, _ := l.Allow(context.Background(), "k"); !res.Allowed {
		t.Fatal("first request should be allowed (bucket starts full)")
	}
	if res, _ := l.Allow(context.Background(), "k"); res.Allowed {
		t.Fatal("second immediate request should be denied")
	}

	clock.Advance(time.Second) // 1 RPS * 1s = 1 token refilled

	res, err := l.Allow(context.Background(), "k")
	if err != nil {
		t.Fatalf("Allow: unexpected error: %v", err)
	}
	if !res.Allowed {
		t.Fatal("request after refill should be allowed")
	}
}

func TestLocal_KeysAreIndependent(t *testing.T) {
	clock := newFakeClock(epoch)
	l := NewLocal(clock, Config{RPS: 1, Burst: 1})

	if res, _ := l.Allow(context.Background(), "a"); !res.Allowed {
		t.Fatal("first request for key a should be allowed")
	}
	if res, _ := l.Allow(context.Background(), "b"); !res.Allowed {
		t.Fatal("first request for key b should be allowed independently of a")
	}
}

func TestLocal_NeverExceedsBurstCapacity(t *testing.T) {
	clock := newFakeClock(epoch)
	l := NewLocal(clock, Config{RPS: 1, Burst: 2})

	l.Allow(context.Background(), "k")
	clock.Advance(time.Hour) // plenty of time to refill way past burst

	res, _ := l.Allow(context.Background(), "k")
	if res.Remaining > 1 { // burst=2, minus the 1 just spent
		t.Errorf("Remaining = %d, want capped so it never exceeds burst-1 after spending one", res.Remaining)
	}
}

func TestLocal_ConcurrentAccessIsSafe(t *testing.T) {
	clock := newFakeClock(epoch)
	l := NewLocal(clock, Config{RPS: 1000, Burst: 1000})

	var wg sync.WaitGroup
	for g := 0; g < 20; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if _, err := l.Allow(context.Background(), "shared-key"); err != nil {
					t.Errorf("Allow: unexpected error: %v", err)
				}
			}
		}()
	}
	wg.Wait()
}

func TestLocal_EvictsIdleBuckets(t *testing.T) {
	clock := newFakeClock(epoch)
	l := NewLocal(clock, Config{RPS: 1, Burst: 1})
	l.idleTTL = time.Minute

	l.Allow(context.Background(), "k")
	sh := l.shardFor("k")
	sh.mu.Lock()
	_, exists := sh.buckets["k"]
	sh.mu.Unlock()
	if !exists {
		t.Fatal("bucket should exist right after Allow")
	}

	clock.Advance(2 * time.Minute)
	l.evictIdle()

	sh.mu.Lock()
	_, exists = sh.buckets["k"]
	sh.mu.Unlock()
	if exists {
		t.Error("bucket should have been evicted after sitting idle past idleTTL")
	}
}

func TestLocal_RunStopsOnContextCancel(t *testing.T) {
	clock := newFakeClock(epoch)
	l := NewLocal(clock, Config{RPS: 1, Burst: 1})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		l.Run(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return promptly after context cancellation")
	}
}
