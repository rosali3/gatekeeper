package breaker

import (
	"sync"
	"testing"
	"time"
)

// fakeClock lets tests advance time deterministically instead of sleeping.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock { return &fakeClock{now: start} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

var epoch = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

func testConfig() Config {
	return Config{
		FailureRatio: 0.5,
		MinRequests:  4,
		Window:       10 * time.Second,
		OpenTimeout:  5 * time.Second,
		HalfOpenMax:  2,
	}
}

func TestCircuitBreaker_StartsClosed(t *testing.T) {
	b := New(newFakeClock(epoch), testConfig())
	if got := b.State(); got != StateClosed {
		t.Errorf("State() = %v, want %v", got, StateClosed)
	}
	if !b.Allow() {
		t.Error("Allow() = false, want true when closed")
	}
}

func TestCircuitBreaker_StaysClosedBelowMinRequests(t *testing.T) {
	b := New(newFakeClock(epoch), testConfig())
	// 3 failures, but minRequests=4 - not enough samples to trip yet.
	for i := 0; i < 3; i++ {
		b.RecordResult(false)
	}
	if got := b.State(); got != StateClosed {
		t.Errorf("State() = %v, want %v (below minRequests)", got, StateClosed)
	}
}

func TestCircuitBreaker_TripsOpenOnFailureRatio(t *testing.T) {
	b := New(newFakeClock(epoch), testConfig())
	// 4 requests, 2 failures = 50% >= failureRatio 0.5, minRequests met.
	b.RecordResult(true)
	b.RecordResult(false)
	b.RecordResult(true)
	b.RecordResult(false)

	if got := b.State(); got != StateOpen {
		t.Fatalf("State() = %v, want %v", got, StateOpen)
	}
	if b.Allow() {
		t.Error("Allow() = true, want false immediately after tripping open")
	}
}

func TestCircuitBreaker_StaysClosedBelowFailureRatio(t *testing.T) {
	b := New(newFakeClock(epoch), testConfig())
	// 4 requests, 1 failure = 25% < failureRatio 0.5.
	b.RecordResult(true)
	b.RecordResult(true)
	b.RecordResult(true)
	b.RecordResult(false)

	if got := b.State(); got != StateClosed {
		t.Errorf("State() = %v, want %v", got, StateClosed)
	}
}

func TestCircuitBreaker_OpenRejectsUntilTimeout(t *testing.T) {
	clock := newFakeClock(epoch)
	b := New(clock, testConfig())
	for i := 0; i < 4; i++ {
		b.RecordResult(false)
	}
	if b.State() != StateOpen {
		t.Fatalf("setup: expected the breaker to be open")
	}

	clock.Advance(4 * time.Second) // openTimeout is 5s - not yet
	if b.Allow() {
		t.Error("Allow() = true before openTimeout elapsed, want false")
	}

	clock.Advance(2 * time.Second) // now 6s since open - past openTimeout
	if !b.Allow() {
		t.Error("Allow() = false after openTimeout elapsed, want true (half-open probe)")
	}
	if got := b.State(); got != StateHalfOpen {
		t.Errorf("State() = %v, want %v", got, StateHalfOpen)
	}
}

func TestCircuitBreaker_HalfOpenLimitsTrialRequests(t *testing.T) {
	clock := newFakeClock(epoch)
	b := New(clock, testConfig()) // halfOpenMax = 2
	for i := 0; i < 4; i++ {
		b.RecordResult(false)
	}
	clock.Advance(6 * time.Second)

	if !b.Allow() { // trial #1
		t.Fatal("Allow() #1 in half-open = false, want true")
	}
	if !b.Allow() { // trial #2
		t.Fatal("Allow() #2 in half-open = false, want true")
	}
	if b.Allow() { // trial #3 - budget of 2 already dispatched
		t.Error("Allow() #3 in half-open = true, want false (halfOpenMax=2 already dispatched)")
	}
}

func TestCircuitBreaker_HalfOpenClosesAfterEnoughSuccesses(t *testing.T) {
	clock := newFakeClock(epoch)
	b := New(clock, testConfig()) // halfOpenMax = 2
	for i := 0; i < 4; i++ {
		b.RecordResult(false)
	}
	clock.Advance(6 * time.Second)

	b.Allow()
	b.Allow()
	b.RecordResult(true)
	if got := b.State(); got != StateHalfOpen {
		t.Fatalf("State() after 1 success = %v, want still %v", got, StateHalfOpen)
	}
	b.RecordResult(true)
	if got := b.State(); got != StateClosed {
		t.Errorf("State() after halfOpenMax successes = %v, want %v", got, StateClosed)
	}
}

func TestCircuitBreaker_HalfOpenReopensOnAnyFailure(t *testing.T) {
	clock := newFakeClock(epoch)
	b := New(clock, testConfig())
	for i := 0; i < 4; i++ {
		b.RecordResult(false)
	}
	clock.Advance(6 * time.Second)

	b.Allow()
	b.RecordResult(false) // a single failure during the trial reopens immediately

	if got := b.State(); got != StateOpen {
		t.Fatalf("State() = %v, want %v", got, StateOpen)
	}
	if b.Allow() {
		t.Error("Allow() = true right after reopening, want false")
	}
}

func TestCircuitBreaker_ClosingResetsTheWindow(t *testing.T) {
	clock := newFakeClock(epoch)
	b := New(clock, testConfig())
	for i := 0; i < 4; i++ {
		b.RecordResult(false)
	}
	clock.Advance(6 * time.Second)
	b.Allow()
	b.Allow()
	b.RecordResult(true)
	b.RecordResult(true) // closes

	// A single failure right after closing must not immediately reopen -
	// the old window should have been cleared, so minRequests isn't met.
	b.RecordResult(false)
	if got := b.State(); got != StateClosed {
		t.Errorf("State() = %v, want %v (stale window must not carry over)", got, StateClosed)
	}
}

func TestCircuitBreaker_OldBucketsAgeOutOfTheWindow(t *testing.T) {
	clock := newFakeClock(epoch)
	b := New(clock, testConfig()) // window = 10s

	b.RecordResult(false)
	b.RecordResult(false)
	clock.Advance(11 * time.Second) // past the window - those failures should no longer count
	b.RecordResult(true)
	b.RecordResult(true)

	if got := b.State(); got != StateClosed {
		t.Errorf("State() = %v, want %v (aged-out failures shouldn't count toward the ratio)", got, StateClosed)
	}
}

func TestCircuitBreaker_StateString(t *testing.T) {
	tests := map[State]string{StateClosed: "closed", StateOpen: "open", StateHalfOpen: "half_open", State(99): "unknown"}
	for state, want := range tests {
		if got := state.String(); got != want {
			t.Errorf("State(%d).String() = %q, want %q", state, got, want)
		}
	}
}
