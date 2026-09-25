package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestID_GeneratesWhenMissing(t *testing.T) {
	var seenInHandler string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenInHandler = r.Header.Get(RequestIDHeader)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	RequestID()(next).ServeHTTP(rec, req)

	if seenInHandler == "" {
		t.Fatal("handler saw no request id")
	}
	if got := rec.Header().Get(RequestIDHeader); got != seenInHandler {
		t.Errorf("response header = %q, want it to match what the handler saw (%q)", got, seenInHandler)
	}
}

func TestRequestID_PreservesExisting(t *testing.T) {
	const existing = "client-supplied-id"
	var seenInHandler string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenInHandler = r.Header.Get(RequestIDHeader)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(RequestIDHeader, existing)
	rec := httptest.NewRecorder()
	RequestID()(next).ServeHTTP(rec, req)

	if seenInHandler != existing {
		t.Errorf("handler saw %q, want the client-supplied %q preserved", seenInHandler, existing)
	}
	if got := rec.Header().Get(RequestIDHeader); got != existing {
		t.Errorf("response header = %q, want %q", got, existing)
	}
}

func TestRequestID_GeneratesUniqueValues(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	mw := RequestID()(next)

	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		id := rec.Header().Get(RequestIDHeader)
		if seen[id] {
			t.Fatalf("duplicate request id generated: %q", id)
		}
		seen[id] = true
	}
}
