package health

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"gatekeeper/internal/balancer"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func targetFor(t *testing.T, srv *httptest.Server) *balancer.Target {
	t.Helper()
	b, err := balancer.New("round_robin", []balancer.TargetSpec{{URL: srv.URL, Weight: 1}})
	if err != nil {
		t.Fatalf("balancer.New: %v", err)
	}
	return b.Targets()[0]
}

func fastConfig(path string) Config {
	return Config{
		Path:               path,
		Interval:           10 * time.Millisecond,
		Timeout:            200 * time.Millisecond,
		HealthyThreshold:   2,
		UnhealthyThreshold: 2,
	}
}

// runChecker starts c.Run in a goroutine bound to a context this test
// cancels on cleanup, so no checker goroutine survives the test - required
// for TestMain's goleak check to pass.
func runChecker(t *testing.T, c *Checker) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() {
		c.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Checker.Run did not return after context cancellation")
		}
	})
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

func TestChecker_MarksTargetUnhealthyThenHealthy(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(false)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if healthy.Load() {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(srv.Close)

	target := targetFor(t, srv)
	checker := NewChecker("demo", fastConfig("/healthz"), []*balancer.Target{target}, testLogger())
	runChecker(t, checker)

	waitFor(t, time.Second, func() bool { return !target.Healthy() })

	healthy.Store(true)
	waitFor(t, time.Second, target.Healthy)
}

func TestChecker_ReportFailure_PassiveCheckMarksUnhealthy(t *testing.T) {
	u, err := url.Parse("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	b, err := balancer.New("round_robin", []balancer.TargetSpec{{URL: u.String(), Weight: 1}})
	if err != nil {
		t.Fatalf("balancer.New: %v", err)
	}
	target := b.Targets()[0]

	cfg := Config{Path: "/healthz", Interval: time.Hour, Timeout: time.Second, HealthyThreshold: 2, UnhealthyThreshold: 2}
	checker := NewChecker("demo", cfg, []*balancer.Target{target}, testLogger())

	if !target.Healthy() {
		t.Fatal("target should start healthy")
	}
	checker.ReportFailure(target, context.DeadlineExceeded)
	if !target.Healthy() {
		t.Fatal("one passive failure should not yet cross unhealthyThreshold=2")
	}
	checker.ReportFailure(target, context.DeadlineExceeded)
	if target.Healthy() {
		t.Fatal("two passive failures should cross unhealthyThreshold=2")
	}
}

func TestChecker_ReportFailure_UnknownTargetIsIgnored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(srv.Close)

	tracked := targetFor(t, srv)
	other := targetFor(t, srv) // not passed to NewChecker below

	checker := NewChecker("demo", fastConfig("/healthz"), []*balancer.Target{tracked}, testLogger())

	// Reporting for a target this checker doesn't know about must not
	// panic (no tracker to look up) and must not affect the target.
	checker.ReportFailure(other, context.DeadlineExceeded)
	if !other.Healthy() {
		t.Error("an unknown target should be left untouched")
	}
}

func TestChecker_StopsOnContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(srv.Close)

	target := targetFor(t, srv)
	checker := NewChecker("demo", fastConfig("/healthz"), []*balancer.Target{target}, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		checker.Run(ctx)
		close(done)
	}()

	time.Sleep(30 * time.Millisecond) // let at least one tick happen
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return promptly after context cancellation")
	}
}

func TestJitter_NonPositiveBaseIsUnchanged(t *testing.T) {
	if got := jitter(0); got != 0 {
		t.Errorf("jitter(0) = %v, want 0", got)
	}
}

func TestJitter_AddsUpToTwentyPercent(t *testing.T) {
	base := 100 * time.Millisecond
	for i := 0; i < 50; i++ {
		got := jitter(base)
		if got < base || got > base+base/5+time.Millisecond {
			t.Errorf("jitter(%v) = %v, want within [base, base+20%%]", base, got)
		}
	}
}
