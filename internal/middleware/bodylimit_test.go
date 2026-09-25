package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBodyLimit_AllowsUnderLimit(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			t.Errorf("unexpected read error: %v", err)
		}
	})

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("hello"))
	BodyLimit(10)(next).ServeHTTP(httptest.NewRecorder(), req)
}

func TestBodyLimit_RejectsOverLimit(t *testing.T) {
	var readErr error
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	})

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("this body is way too long"))
	BodyLimit(5)(next).ServeHTTP(httptest.NewRecorder(), req)

	if readErr == nil {
		t.Fatal("expected a read error once the body limit is exceeded")
	}
}
