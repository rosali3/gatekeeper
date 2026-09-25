package health

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"gatekeeper/internal/balancer"
)

// Config is one upstream's health_check settings.
type Config struct {
	Path               string
	Interval           time.Duration
	Timeout            time.Duration
	HealthyThreshold   int
	UnhealthyThreshold int
}

// Checker runs active health checks for every target of one upstream, and
// also accepts passive failure reports from the proxy. One Checker maps to
// one upstream and one goroutine (via Run), per spec.
type Checker struct {
	upstreamName string
	cfg          Config
	targets      []*balancer.Target
	trackers     map[*balancer.Target]*Tracker
	client       *http.Client
	log          *slog.Logger
}

func NewChecker(upstreamName string, cfg Config, targets []*balancer.Target, log *slog.Logger) *Checker {
	trackers := make(map[*balancer.Target]*Tracker, len(targets))
	for _, t := range targets {
		trackers[t] = NewTracker(t, cfg.HealthyThreshold, cfg.UnhealthyThreshold)
	}
	return &Checker{
		upstreamName: upstreamName,
		cfg:          cfg,
		targets:      targets,
		trackers:     trackers,
		client:       &http.Client{},
		log:          log,
	}
}

// Run actively checks all targets on a jittered interval until ctx is
// canceled. Each tick's per-target checks are joined (wg.Wait) before the
// next tick is scheduled, so canceling ctx never leaves a check goroutine
// running past Run's return - there's nothing left to leak.
func (c *Checker) Run(ctx context.Context) {
	timer := time.NewTimer(jitter(c.cfg.Interval))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			c.checkAll(ctx)
			timer.Reset(jitter(c.cfg.Interval))
		}
	}
}

func (c *Checker) checkAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, t := range c.targets {
		wg.Add(1)
		go func(t *balancer.Target) {
			defer wg.Done()
			c.checkOne(ctx, t)
		}(t)
	}
	wg.Wait()
}

func (c *Checker) checkOne(ctx context.Context, t *balancer.Target) {
	checkCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	healthURL := *t.URL
	healthURL.Path = c.cfg.Path
	healthURL.RawQuery = ""

	req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, healthURL.String(), nil)
	if err != nil {
		c.ReportFailure(t, err)
		return
	}

	resp, err := c.client.Do(req)
	if err != nil {
		c.ReportFailure(t, err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body) // drain so the connection can be reused

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.ReportFailure(t, fmt.Errorf("unexpected status %d", resp.StatusCode))
		return
	}
	c.ReportSuccess(t)
}

// ReportSuccess records a successful check for t (used by active checks).
func (c *Checker) ReportSuccess(t *balancer.Target) {
	tracker, ok := c.trackers[t]
	if !ok {
		return
	}
	if tracker.ReportSuccess() {
		c.log.Info("target marked healthy", "upstream", c.upstreamName, "target", t.URL.String())
	}
}

// ReportFailure records a failed check for t. It's used both by active
// checks and by the gateway's proxy ErrorHandler as a passive check - a
// proxy error counts toward the same unhealthy_threshold as a failed
// active probe.
func (c *Checker) ReportFailure(t *balancer.Target, err error) {
	tracker, ok := c.trackers[t]
	if !ok {
		return
	}
	if tracker.ReportFailure() {
		c.log.Warn("target marked unhealthy", "upstream", c.upstreamName, "target", t.URL.String(), "error", err)
	}
}

// jitter adds up to ~20% random skew to base, so many upstreams/targets
// checked on the same interval don't all fire in lockstep.
func jitter(base time.Duration) time.Duration {
	if base <= 0 {
		return base
	}
	return base + time.Duration(rand.Int63n(int64(base)/5+1))
}
