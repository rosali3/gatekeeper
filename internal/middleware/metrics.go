package middleware

import (
	"net/http"
	"strconv"
	"time"

	"gatekeeper/internal/metrics"
	"gatekeeper/internal/reqctx"
)

// Metrics records gateway_requests_total and gateway_request_duration_
// seconds. It wraps everything downstream of it (per the spec's
// middleware order, right after logging) so the route label - only known
// once route-match has run - is available by the time it reads
// reqctx.AccessFields back out, the same pattern AccessLog uses.
//
// It keeps its own status-recording wrapper rather than sharing
// AccessLog's: the two middlewares don't have a way to hand a captured
// status code to each other across their separate closures, and
// duplicating a tiny recorder is simpler than inventing one.
func Metrics() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r)

			route := ""
			if fields := reqctx.AccessFieldsFrom(r.Context()); fields != nil {
				route = fields.Route
			}
			metrics.RequestsTotal.WithLabelValues(route, strconv.Itoa(rec.status)).Inc()
			metrics.RequestDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())
		})
	}
}
