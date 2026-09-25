package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/goleak"

	"gatekeeper/internal/config"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func testConfig(upstreamURL string) *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Listen:            ":0",
			ReadHeaderTimeout: config.Duration(5 * time.Second),
			MaxBodyBytes:      config.ByteSize(1 << 20),
		},
		Upstreams: map[string]config.UpstreamConfig{
			"demo": {
				Balancer: config.BalancerRoundRobin,
				Targets:  []config.Target{{URL: upstreamURL, Weight: 1}},
				Timeout:  config.Duration(5 * time.Second),
				HealthCheck: config.HealthCheckConfig{
					Path:               "/",
					Interval:           config.Duration(time.Hour),
					Timeout:            config.Duration(time.Second),
					HealthyThreshold:   1,
					UnhealthyThreshold: 1,
				},
			},
		},
		Routes: []config.RouteConfig{
			{
				Match:    config.MatchConfig{PathPrefix: "/"},
				Upstream: "demo",
				Auth:     config.AuthNone,
			},
		},
	}
}

// TestRun_GracefulShutdownWaitsForInFlightRequest is the automated version
// of "graceful shutdown does not break active requests": an OS SIGTERM is
// unreliable to deliver from a test on Windows, so this cancels run()'s
// context directly instead, which is exactly what the signal handler in
// main() does on receipt of SIGTERM/os.Interrupt.
func TestRun_GracefulShutdownWaitsForInFlightRequest(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-releaseRequest
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := ln.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- run(ctx, ln, testConfig(backend.URL), testLogger()) }()

	// Wait for the server to actually be accepting connections.
	waitForServer(t, addr)

	reqDone := make(chan struct{})
	go func() {
		resp, err := http.Get("http://" + addr + "/slow")
		if err != nil {
			t.Errorf("in-flight request failed: %v", err)
		} else {
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("in-flight request status = %d, want %d", resp.StatusCode, http.StatusOK)
			}
		}
		close(reqDone)
	}()

	<-requestStarted // the request is now in flight against the backend
	cancel()         // simulate a shutdown signal

	// The request must still be allowed to finish even though shutdown has
	// started; only after releasing it should run() return.
	select {
	case <-reqDone:
		t.Fatal("in-flight request finished before it was released - shutdown didn't wait for it")
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseRequest)

	select {
	case <-reqDone:
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight request never completed after being released")
	}

	select {
	case err := <-runErr:
		if err != nil {
			t.Errorf("run() returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run() did not return after the in-flight request completed")
	}
}

func waitForServer(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server at %s never became reachable", addr)
}
