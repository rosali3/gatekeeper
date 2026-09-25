package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"gatekeeper/internal/reqctx"
)

// statusRecorder captures the status code so it can be logged after the
// handler returns; http.ResponseWriter doesn't expose what was written.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// AccessLog logs one JSON line per request. It must wrap everything that
// can populate reqctx.AccessFields (route matching, proxying), so route,
// upstream and target are only present in the log once those inner layers
// have run.
func AccessLog(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ctx, fields := reqctx.WithAccessFields(r.Context())
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r.WithContext(ctx))

			log.Info("access",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", r.Header.Get(RequestIDHeader),
				"remote_addr", r.RemoteAddr,
				"route", fields.Route,
				"upstream", fields.Upstream,
				"target", fields.Target,
				"user", fields.UserID,
			)
		})
	}
}
