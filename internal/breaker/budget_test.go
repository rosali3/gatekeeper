package breaker

import (
	"testing"
	"time"
)

func TestRetryBudget_DeniesWithNoRequestsRecorded(t *testing.T) {
	b := NewRetryBudget(newFakeClock(epoch), 0.5)
	if b.AllowRetry() {
		t.Error("AllowRetry() = true with zero recorded requests, want false")
	}
}

func TestRetryBudget_AllowsWithinRatio(t *testing.T) {
	b := NewRetryBudget(newFakeClock(epoch), 0.5)
	for i := 0; i < 10; i++ {
		b.RecordRequest()
	}
	// ratio 0.5 of 10 requests allows up to 5 retries.
	for i := 0; i < 5; i++ {
		if !b.AllowRetry() {
			t.Fatalf("AllowRetry() #%d = false, want true (within budget)", i)
		}
	}
}

func TestRetryBudget_DeniesOnceRatioExceeded(t *testing.T) {
	b := NewRetryBudget(newFakeClock(epoch), 0.2)
	for i := 0; i < 10; i++ {
		b.RecordRequest()
	}
	// ratio 0.2 of 10 requests allows 2 retries; the 3rd would push
	// (retries+1)/requests = 3/10 = 0.3 > 0.2.
	if !b.AllowRetry() {
		t.Fatal("AllowRetry() #1 = false, want true")
	}
	if !b.AllowRetry() {
		t.Fatal("AllowRetry() #2 = false, want true")
	}
	if b.AllowRetry() {
		t.Error("AllowRetry() #3 = true, want false (budget exhausted)")
	}
}

func TestRetryBudget_RequestsOutsideWindowDontCount(t *testing.T) {
	clock := newFakeClock(epoch)
	b := NewRetryBudget(clock, 0.5)
	for i := 0; i < 10; i++ {
		b.RecordRequest()
	}
	clock.Advance(budgetWindow + time.Second)

	// The old requests have aged out, so there's no recorded baseline
	// left in the window - budget denies by default (see AllowRetry doc).
	if b.AllowRetry() {
		t.Error("AllowRetry() = true after all requests aged out of the window, want false")
	}
}

func TestRetryBudget_MoreRequestsRaiseTheCeiling(t *testing.T) {
	b := NewRetryBudget(newFakeClock(epoch), 0.5)
	for i := 0; i < 4; i++ {
		b.RecordRequest()
	}
	if !b.AllowRetry() { // 1/4 = 0.25, fine
		t.Fatal("AllowRetry() #1 = false, want true")
	}
	if !b.AllowRetry() { // 2/4 = 0.5, still within ratio (>, not >=)
		t.Fatal("AllowRetry() #2 = false, want true")
	}
	if b.AllowRetry() { // 3/4 = 0.75 > 0.5
		t.Error("AllowRetry() #3 = true, want false")
	}

	// More requests raise the denominator, making room for another retry.
	for i := 0; i < 4; i++ {
		b.RecordRequest()
	}
	if !b.AllowRetry() { // 3/8 = 0.375 <= 0.5
		t.Error("AllowRetry() after more requests = false, want true")
	}
}
