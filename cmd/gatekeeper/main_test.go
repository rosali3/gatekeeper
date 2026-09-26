package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
				CircuitBreaker: config.CircuitBreakerConfig{
					FailureRatio: 0.5,
					MinRequests:  1000,
					Window:       config.Duration(10 * time.Second),
					OpenTimeout:  config.Duration(time.Second),
					HalfOpenMax:  1,
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

func mustListen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
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

	mainLn := mustListen(t)
	adminLn := mustListen(t)
	addr := mainLn.Addr().String()
	cfg := testConfig(backend.URL)

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() {
		runErr <- run(ctx, mainLn, adminLn, cfg, "/nonexistent/gatekeeper.yaml", "test-token", testLogger())
	}()

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

func namedBackend(t *testing.T, name string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", name)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const reloadTestConfigTemplate = `
server:
  listen: ":18080"
  read_header_timeout: 5s
  max_body_bytes: 1MB
admin:
  listen: ":19090"
  token_env: TEST_GATEKEEPER_ADMIN_TOKEN
upstreams:
  demo:
    balancer: round_robin
    targets:
      - { url: %q, weight: 1 }
    health_check: { path: /, interval: 1h, timeout: 1s, healthy_threshold: 1, unhealthy_threshold: 1 }
    circuit_breaker: { failure_ratio: 0.5, min_requests: 1000, window: 10s, open_timeout: 1s, half_open_max: 1 }
    timeout: 5s
routes:
  - match: { path_prefix: /api/ }
    upstream: demo
    auth: none
`

func writeReloadTestConfig(t *testing.T, path, upstreamURL string) {
	t.Helper()
	content := fmt.Sprintf(reloadTestConfigTemplate, upstreamURL)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestRun_ReloadViaFileChange(t *testing.T) {
	t.Setenv("TEST_GATEKEEPER_ADMIN_TOKEN", "test-token")

	backendA := namedBackend(t, "backend-a")
	backendB := namedBackend(t, "backend-b")

	configPath := filepath.Join(t.TempDir(), "gatekeeper.yaml")
	writeReloadTestConfig(t, configPath, backendA.URL)

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	mainLn := mustListen(t)
	adminLn := mustListen(t)
	addr := mainLn.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runErr := make(chan error, 1)
	go func() { runErr <- run(ctx, mainLn, adminLn, cfg, configPath, "test-token", testLogger()) }()
	waitForServer(t, addr)

	resp, err := http.Get("http://" + addr + "/api/x")
	if err != nil {
		t.Fatalf("initial request: %v", err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("X-Backend"); got != "backend-a" {
		t.Fatalf("initial request hit %q, want backend-a", got)
	}

	// Rewrite the config to point at a different backend and wait for
	// fsnotify + the debounce + reload to take effect.
	writeReloadTestConfig(t, configPath, backendB.URL)

	deadline := time.Now().Add(3 * time.Second)
	var lastBackend string
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/api/x")
		if err == nil {
			lastBackend = resp.Header.Get("X-Backend")
			resp.Body.Close()
			if lastBackend == "backend-b" {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if lastBackend != "backend-b" {
		t.Fatalf("after editing the config file, requests still hit %q, want backend-b", lastBackend)
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Errorf("run() returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run() did not return after shutdown")
	}
}

func TestRun_ReloadViaAdminEndpoint(t *testing.T) {
	t.Setenv("TEST_GATEKEEPER_ADMIN_TOKEN", "test-token")

	backendA := namedBackend(t, "backend-a")
	backendB := namedBackend(t, "backend-b")

	configPath := filepath.Join(t.TempDir(), "gatekeeper.yaml")
	writeReloadTestConfig(t, configPath, backendA.URL)

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	mainLn := mustListen(t)
	adminLn := mustListen(t)
	mainAddr := mainLn.Addr().String()
	adminAddr := adminLn.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runErr := make(chan error, 1)
	go func() { runErr <- run(ctx, mainLn, adminLn, cfg, configPath, "test-token", testLogger()) }()
	waitForServer(t, mainAddr)
	waitForServer(t, adminAddr)

	// Change the file, but reload via the admin endpoint rather than
	// waiting for fsnotify - proves POST /admin/reload independently
	// re-reads the same config path.
	writeReloadTestConfig(t, configPath, backendB.URL)

	req, err := http.NewRequest(http.MethodPost, "http://"+adminAddr+"/admin/reload", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	reloadResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /admin/reload: %v", err)
	}
	reloadResp.Body.Close()
	if reloadResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /admin/reload status = %d, want %d", reloadResp.StatusCode, http.StatusOK)
	}

	resp, err := http.Get("http://" + mainAddr + "/api/x")
	if err != nil {
		t.Fatalf("request after reload: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Backend"); got != "backend-b" {
		t.Errorf("after admin reload, request hit %q, want backend-b", got)
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Errorf("run() returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run() did not return after shutdown")
	}
}

func TestRun_ReloadWithBadConfigKeepsOldOne(t *testing.T) {
	t.Setenv("TEST_GATEKEEPER_ADMIN_TOKEN", "test-token")

	backendA := namedBackend(t, "backend-a")

	configPath := filepath.Join(t.TempDir(), "gatekeeper.yaml")
	writeReloadTestConfig(t, configPath, backendA.URL)

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	mainLn := mustListen(t)
	adminLn := mustListen(t)
	mainAddr := mainLn.Addr().String()
	adminAddr := adminLn.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runErr := make(chan error, 1)
	go func() { runErr <- run(ctx, mainLn, adminLn, cfg, configPath, "test-token", testLogger()) }()
	waitForServer(t, mainAddr)
	waitForServer(t, adminAddr)

	// Corrupt the config file, then trigger a reload via the admin API.
	if err := os.WriteFile(configPath, []byte("not: [valid"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "http://"+adminAddr+"/admin/reload", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	reloadResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /admin/reload: %v", err)
	}
	reloadResp.Body.Close()
	if reloadResp.StatusCode != http.StatusInternalServerError {
		t.Errorf("POST /admin/reload status = %d, want %d (invalid config)", reloadResp.StatusCode, http.StatusInternalServerError)
	}

	// The gateway must still be serving the last-good config.
	resp, err := http.Get("http://" + mainAddr + "/api/x")
	if err != nil {
		t.Fatalf("request after failed reload: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Backend"); got != "backend-a" {
		t.Errorf("after a failed reload, request hit %q, want backend-a (old config should still be active)", got)
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Errorf("run() returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run() did not return after shutdown")
	}
}
