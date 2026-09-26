package main

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"gatekeeper/internal/gateway"
	"gatekeeper/internal/metrics"
)

func TestRefreshMetricsPeriodically_TicksUntilCanceled(t *testing.T) {
	srv := namedBackend(t, "backend-a")
	buildCtx, buildCancel := context.WithCancel(context.Background())
	t.Cleanup(buildCancel)

	snap, err := gateway.Build(buildCtx, testConfig(srv.URL), testLogger(), nil)
	if err != nil {
		t.Fatalf("gateway.Build: %v", err)
	}
	gw := gateway.New()
	gw.Swap(snap)

	target := snap.Upstreams["demo"].Balancer.Targets()[0]

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		refreshMetricsPeriodically(ctx, gw, 10*time.Millisecond)
		close(done)
	}()

	deadline := time.Now().Add(time.Second)
	var got float64
	for time.Now().Before(deadline) {
		got = testutil.ToFloat64(metrics.UpstreamHealthy.WithLabelValues("demo", target.URL.String()))
		if got == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got != 1 {
		t.Fatalf("UpstreamHealthy = %v, want 1 within 1s of ticking", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("refreshMetricsPeriodically did not return after context cancellation")
	}
}
