package gateway

import (
	"bytes"
	"net/http"
)

// isIdempotentMethod matches the TZ's fixed retryable-method list
// exactly. only_idempotent: false in config is an explicit opt-out of
// this restriction, checked by the caller - this function only answers
// "is this method idempotent," not "should this request be retried."
func isIdempotentMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete:
		return true
	default:
		return false
	}
}

// isRetryableStatus is the TZ's retry trigger: network errors are already
// mapped to 502/504 by the proxy's ErrorHandler, so checking these three
// status codes covers "network error or 502/503/504" in one place. A
// plain 500 from a reachable, responding upstream is deliberately not
// included - retrying an application error rarely helps and isn't what
// the spec asks for.
func isRetryableStatus(status int) bool {
	return status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

// statusRecordingWriter passes writes straight through to the real
// ResponseWriter - no buffering - while recording the status code, so the
// no-retry fast path can still feed the circuit breaker without paying
// for a body copy on every request.
type statusRecordingWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusRecordingWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// bufferedResponse captures a whole attempt's response instead of writing
// it through immediately, so a retryable failure can be discarded rather
// than having already been partially sent to the real client. Only used
// on the retry-capable path - see proxyHandler.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newBufferedResponse() *bufferedResponse {
	return &bufferedResponse{header: make(http.Header)}
}

func (r *bufferedResponse) Header() http.Header { return r.header }

func (r *bufferedResponse) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(b)
}

func (r *bufferedResponse) WriteHeader(code int) { r.status = code }

// copyTo flushes the buffered attempt to the real ResponseWriter, once a
// final outcome (success, non-retryable failure, or attempts/budget
// exhausted) has been decided.
func (r *bufferedResponse) copyTo(w http.ResponseWriter) {
	dst := w.Header()
	for k, vv := range r.header {
		dst[k] = vv
	}
	status := r.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(r.body.Bytes())
}
