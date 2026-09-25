package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestNewMux_Healthy(t *testing.T) {
	mux := newMux("svc", 0, 0, true)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("GET /healthz = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestNewMux_Unhealthy(t *testing.T) {
	mux := newMux("svc", 0, 0, false)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /healthz = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestNewMux_EchoesRequest(t *testing.T) {
	mux := newMux("svc", 0, 0, true)

	req := httptest.NewRequest(http.MethodPost, "/api/foo", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/foo = %d, want %d", rec.Code, http.StatusOK)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if body["upstream"] != "svc" || body["method"] != http.MethodPost || body["path"] != "/api/foo" {
		t.Errorf("unexpected body: %+v", body)
	}
}

func TestNewMux_AlwaysErrors(t *testing.T) {
	mux := newMux("svc", 0, 1, true)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("error_rate=1: GET / = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestNewMux_AppliesDelay(t *testing.T) {
	mux := newMux("svc", 20*time.Millisecond, 0, true)

	start := time.Now()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if elapsed < 20*time.Millisecond {
		t.Errorf("elapsed = %v, want >= 20ms", elapsed)
	}
}

func TestGetEnv_Defaults(t *testing.T) {
	const key = "GATEKEEPER_TEST_UNSET_VAR"
	os.Unsetenv(key)
	if got := getEnv(key, "fallback"); got != "fallback" {
		t.Errorf("getEnv default = %q, want %q", got, "fallback")
	}
}

func TestGetEnv_Overrides(t *testing.T) {
	const key = "GATEKEEPER_TEST_SET_VAR"
	t.Setenv(key, "value")
	if got := getEnv(key, "fallback"); got != "value" {
		t.Errorf("getEnv override = %q, want %q", got, "value")
	}
}

func TestGetEnvDuration(t *testing.T) {
	const key = "GATEKEEPER_TEST_DELAY"
	t.Setenv(key, "150ms")
	if got, want := getEnvDuration(key, 0), 150*time.Millisecond; got != want {
		t.Errorf("getEnvDuration = %v, want %v", got, want)
	}
}

func TestGetEnvFloat(t *testing.T) {
	const key = "GATEKEEPER_TEST_RATE"
	t.Setenv(key, "0.25")
	if got, want := getEnvFloat(key, 0), 0.25; got != want {
		t.Errorf("getEnvFloat = %v, want %v", got, want)
	}
}

func TestGetEnvBool(t *testing.T) {
	const key = "GATEKEEPER_TEST_HEALTHY"
	t.Setenv(key, "false")
	if got := getEnvBool(key, true); got != false {
		t.Errorf("getEnvBool = %v, want %v", got, false)
	}
}
