package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"

	"gatekeeper/internal/balancer"
	"gatekeeper/internal/health"
)

// targetContextKey carries the balancer.Target picked for this request
// (by proxyHandler) through to the ReverseProxy's Rewrite func.
type targetContextKey struct{}

// newReverseProxy builds the shared proxy for one upstream. The actual
// target is not fixed here - it's read per request from context, since
// which target to use depends on the balancer's per-request pick.
//
// Hop-by-hop headers (Connection, Keep-Alive, Transfer-Encoding, ...) are
// stripped by httputil.ReverseProxy itself after Rewrite runs, so this code
// doesn't need to (and must not try to) do that by hand.
// newReverseProxy builds the shared proxy for one upstream. checker is
// notified of proxy errors (the passive half of health checking): a
// failure here counts toward the same unhealthy_threshold as a failed
// active probe.
func newReverseProxy(name string, transport http.RoundTripper, log *slog.Logger, checker *health.Checker) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			// proxyHandler always sets this before calling ServeHTTP.
			target := pr.In.Context().Value(targetContextKey{}).(*balancer.Target)
			pr.SetXForwarded()
			pr.SetURL(target.URL)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			status := http.StatusBadGateway
			if errors.Is(err, context.DeadlineExceeded) {
				status = http.StatusGatewayTimeout
			}
			if target, ok := r.Context().Value(targetContextKey{}).(*balancer.Target); ok {
				checker.ReportFailure(target, err)
			}
			log.Error("upstream error", "upstream", name, "error", err)
			w.WriteHeader(status)
		},
	}
}
