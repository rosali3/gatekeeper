package gateway

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"gatekeeper/internal/config"
)

// statusBackend always answers with a fixed status code and counts how
// many times it was actually hit.
func statusBackend(t *testing.T, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func configWithRetries(upstreamURL string, targets []config.Target, retries config.RetriesConfig, cb config.CircuitBreakerConfig) *config.Config {
	cfg := baseConfig(upstreamURL)
	up := cfg.Upstreams["demo"]
	up.Targets = targets
	up.Retries = retries
	up.CircuitBreaker = cb
	cfg.Upstreams["demo"] = up
	return cfg
}

func permissiveBreaker() config.CircuitBreakerConfig {
	return config.CircuitBreakerConfig{
		FailureRatio: 0.5, MinRequests: 1000, Window: config.Duration(10 * time.Second),
		OpenTimeout: config.Duration(time.Second), HalfOpenMax: 1,
	}
}

func TestGateway_Retry_SucceedsOnSecondTarget(t *testing.T) {
	bad, badHits := statusBackend(t, http.StatusServiceUnavailable)
	good, goodHits := statusBackend(t, http.StatusOK)

	cfg := configWithRetries(bad.URL,
		[]config.Target{{URL: bad.URL, Weight: 1}, {URL: good.URL, Weight: 1}},
		config.RetriesConfig{Max: 1, OnlyIdempotent: true, BudgetRatio: 1.0},
		permissiveBreaker(),
	)

	gw := New()
	gw.Swap(buildSnapshot(t, cfg))

	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/demo/x", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if badHits.Load() != 1 || goodHits.Load() != 1 {
		t.Errorf("badHits=%d goodHits=%d, want exactly one attempt at each target", badHits.Load(), goodHits.Load())
	}
}

func TestGateway_Retry_StopsAtMaxAttempts(t *testing.T) {
	bad, badHits := statusBackend(t, http.StatusServiceUnavailable)

	cfg := configWithRetries(bad.URL,
		[]config.Target{{URL: bad.URL, Weight: 1}},
		config.RetriesConfig{Max: 2, OnlyIdempotent: true, BudgetRatio: 1.0},
		permissiveBreaker(),
	)

	snap := buildSnapshot(t, cfg)
	gw := New()
	gw.Swap(snap)

	// The retry budget's denominator is *request* volume, not attempts
	// within one request - budget_ratio=1.0 against a single recorded
	// request only ever clears one retry (see AllowRetry's doc comment).
	// Seed enough baseline volume that this test is actually exercising
	// "stops at max attempts," not "stops because of the budget."
	snap.Upstreams["demo"].retryBudget.RecordRequest()

	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/demo/x", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	// 1 initial attempt + Max=2 retries = 3 total.
	if got := badHits.Load(); got != 3 {
		t.Errorf("backend hit %d times, want 3 (1 initial + 2 retries)", got)
	}
}

func TestGateway_Retry_NonIdempotentMethodNotRetried(t *testing.T) {
	bad, badHits := statusBackend(t, http.StatusServiceUnavailable)
	good, goodHits := statusBackend(t, http.StatusOK)

	cfg := configWithRetries(bad.URL,
		[]config.Target{{URL: bad.URL, Weight: 1}, {URL: good.URL, Weight: 1}},
		config.RetriesConfig{Max: 1, OnlyIdempotent: true, BudgetRatio: 1.0},
		permissiveBreaker(),
	)

	gw := New()
	gw.Swap(buildSnapshot(t, cfg))

	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/demo/x", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d (POST must not be retried)", rec.Code, http.StatusServiceUnavailable)
	}
	if badHits.Load() != 1 {
		t.Errorf("bad target hit %d times, want 1 (no retry)", badHits.Load())
	}
	if goodHits.Load() != 0 {
		t.Errorf("good target hit %d times, want 0 (never reached without a retry)", goodHits.Load())
	}
}

func TestGateway_Retry_NonRetryableStatusNotRetried(t *testing.T) {
	bad, badHits := statusBackend(t, http.StatusInternalServerError) // 500, not in the retryable set
	good, goodHits := statusBackend(t, http.StatusOK)

	cfg := configWithRetries(bad.URL,
		[]config.Target{{URL: bad.URL, Weight: 1}, {URL: good.URL, Weight: 1}},
		config.RetriesConfig{Max: 1, OnlyIdempotent: true, BudgetRatio: 1.0},
		permissiveBreaker(),
	)

	gw := New()
	gw.Swap(buildSnapshot(t, cfg))

	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/demo/x", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d (500 is not retryable)", rec.Code, http.StatusInternalServerError)
	}
	if badHits.Load() != 1 || goodHits.Load() != 0 {
		t.Errorf("badHits=%d goodHits=%d, want 1/0 (no retry on a plain 500)", badHits.Load(), goodHits.Load())
	}
}

func TestGateway_Retry_DisabledWhenMaxIsZero(t *testing.T) {
	bad, badHits := statusBackend(t, http.StatusServiceUnavailable)

	cfg := configWithRetries(bad.URL,
		[]config.Target{{URL: bad.URL, Weight: 1}},
		config.RetriesConfig{Max: 0, OnlyIdempotent: true, BudgetRatio: 1.0},
		permissiveBreaker(),
	)

	gw := New()
	gw.Swap(buildSnapshot(t, cfg))

	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/demo/x", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if got := badHits.Load(); got != 1 {
		t.Errorf("backend hit %d times, want 1 (retries.max=0 disables retrying)", got)
	}
}

func TestGateway_Retry_BudgetExhaustionStopsRetrying(t *testing.T) {
	bad, badHits := statusBackend(t, http.StatusServiceUnavailable)
	good, goodHits := statusBackend(t, http.StatusOK)

	cfg := configWithRetries(bad.URL,
		[]config.Target{{URL: bad.URL, Weight: 1}, {URL: good.URL, Weight: 1}},
		// budget_ratio=0 means AllowRetry() can never approve a retry once
		// at least one request has been recorded.
		config.RetriesConfig{Max: 1, OnlyIdempotent: true, BudgetRatio: 0},
		permissiveBreaker(),
	)

	gw := New()
	gw.Swap(buildSnapshot(t, cfg))

	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/demo/x", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if badHits.Load() != 1 {
		t.Errorf("bad target hit %d times, want 1 (retry budget exhausted immediately)", badHits.Load())
	}
	if goodHits.Load() != 0 {
		t.Errorf("good target hit %d times, want 0", goodHits.Load())
	}
}

func TestGateway_CircuitBreaker_OpensAndSkipsTheUpstream(t *testing.T) {
	bad, badHits := statusBackend(t, http.StatusServiceUnavailable)

	cfg := configWithRetries(bad.URL,
		[]config.Target{{URL: bad.URL, Weight: 1}},
		config.RetriesConfig{Max: 0},
		config.CircuitBreakerConfig{
			FailureRatio: 0.5, MinRequests: 2, Window: config.Duration(10 * time.Second),
			OpenTimeout: config.Duration(time.Minute), HalfOpenMax: 1,
		},
	)

	gw := New()
	gw.Swap(buildSnapshot(t, cfg))

	// Two failing requests meet minRequests=2 at a 100% failure ratio and
	// trip the breaker open.
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		gw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/demo/x", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("request #%d status = %d, want %d", i, rec.Code, http.StatusServiceUnavailable)
		}
	}
	if got := badHits.Load(); got != 2 {
		t.Fatalf("backend hit %d times before tripping, want 2", got)
	}

	// The breaker is now open (openTimeout=1m, so it stays open) - further
	// requests must be rejected without ever reaching the backend again.
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		gw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/demo/x", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("post-trip request #%d status = %d, want %d", i, rec.Code, http.StatusServiceUnavailable)
		}
	}
	if got := badHits.Load(); got != 2 {
		t.Errorf("backend hit %d times after tripping, want still 2 (breaker should short-circuit)", got)
	}
}

func TestGateway_CircuitBreaker_PUTIsRetriedAndIsIdempotent(t *testing.T) {
	// PUT is in the idempotent set, so with only_idempotent: true it
	// should still be retried, unlike POST.
	bad, badHits := statusBackend(t, http.StatusBadGateway)
	good, goodHits := statusBackend(t, http.StatusOK)

	cfg := configWithRetries(bad.URL,
		[]config.Target{{URL: bad.URL, Weight: 1}, {URL: good.URL, Weight: 1}},
		config.RetriesConfig{Max: 1, OnlyIdempotent: true, BudgetRatio: 1.0},
		permissiveBreaker(),
	)

	gw := New()
	gw.Swap(buildSnapshot(t, cfg))

	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/demo/x", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if badHits.Load() != 1 || goodHits.Load() != 1 {
		t.Errorf("badHits=%d goodHits=%d, want 1/1 (PUT is idempotent, should retry)", badHits.Load(), goodHits.Load())
	}
}
