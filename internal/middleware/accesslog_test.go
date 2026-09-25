package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"gatekeeper/internal/reqctx"
)

func TestAccessLog_RecordsStatusAndFields(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fields := reqctx.AccessFieldsFrom(r.Context())
		fields.Route = "/api/"
		fields.Upstream = "floorplan"
		fields.Target = "http://backend-1:8080"
		w.WriteHeader(http.StatusCreated)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/x", nil)
	req.Header.Set(RequestIDHeader, "rid-123")
	AccessLog(log)(next).ServeHTTP(httptest.NewRecorder(), req)

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log output is not valid JSON: %v\n%s", err, buf.String())
	}
	checks := map[string]any{
		"status":     float64(http.StatusCreated),
		"method":     http.MethodPost,
		"path":       "/api/x",
		"request_id": "rid-123",
		"route":      "/api/",
		"upstream":   "floorplan",
		"target":     "http://backend-1:8080",
	}
	for k, want := range checks {
		if line[k] != want {
			t.Errorf("field %q = %v, want %v", k, line[k], want)
		}
	}
}

func TestAccessLog_DefaultsStatusTo200WhenNeverWritten(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

	AccessLog(log)(next).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log output is not valid JSON: %v", err)
	}
	if line["status"] != float64(http.StatusOK) {
		t.Errorf("status = %v, want %v", line["status"], http.StatusOK)
	}
}
