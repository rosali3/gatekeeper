// Package admin implements gatekeeper's admin API: a separate,
// Bearer-token-protected HTTP handler exposing route/upstream
// introspection and a manual reload trigger, plus unauthenticated
// /healthz, /readyz and /metrics for orchestrators and Prometheus.
package admin

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"gatekeeper/internal/gateway"
)

// gatewayView is the minimal slice of *gateway.Gateway this package
// needs, so tests can supply a fake without spinning up a real one.
type gatewayView interface {
	Current() *gateway.Snapshot
}

// NewHandler builds the admin HTTP handler. token is the expected Bearer
// token (the caller reads it from the env var named by admin.token_env);
// reload is called by POST /admin/reload.
//
// Per the TZ, admin endpoints sit behind a Bearer token - but /healthz,
// /readyz and /metrics deliberately don't: orchestrator liveness/
// readiness probes and Prometheus scrapes conventionally hit those
// without credentials, and requiring a token there is more likely to
// break monitoring than to add real protection for endpoints that leak
// no sensitive config.
func NewHandler(gw gatewayView, reload func() error, token string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/readyz", handleReadyz(gw))
	mux.Handle("/metrics", promhttp.Handler())

	mux.Handle("/admin/routes", bearerAuth(token, http.HandlerFunc(handleRoutes(gw))))
	mux.Handle("/admin/upstreams", bearerAuth(token, http.HandlerFunc(handleUpstreams(gw))))
	mux.Handle("/admin/reload", bearerAuth(token, http.HandlerFunc(handleReload(reload))))

	return mux
}

// bearerAuth checks Authorization: Bearer <token> in constant time, same
// rationale as internal/auth's API key comparison.
func bearerAuth(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, prefix) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		provided := strings.TrimPrefix(h, prefix)
		if subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// handleReadyz reports ready once the gateway has loaded a config at
// least once - liveness (/healthz) and readiness are different questions
// during the brief startup window before the first Build/Swap completes.
func handleReadyz(gw gatewayView) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if gw.Current() == nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func handleRoutes(gw gatewayView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		snap := gw.Current()
		if snap == nil {
			http.Error(w, "gateway not ready", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, snap.RouteConfigs())
	}
}

type targetView struct {
	URL         string `json:"url"`
	Healthy     bool   `json:"healthy"`
	ActiveConns int64  `json:"active_conns"`
}

type upstreamView struct {
	Name         string       `json:"name"`
	BreakerState string       `json:"breaker_state"`
	Targets      []targetView `json:"targets"`
}

func handleUpstreams(gw gatewayView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		snap := gw.Current()
		if snap == nil {
			http.Error(w, "gateway not ready", http.StatusServiceUnavailable)
			return
		}

		views := make([]upstreamView, 0, len(snap.Upstreams))
		for name, up := range snap.Upstreams {
			targets := up.Balancer.Targets()
			tviews := make([]targetView, 0, len(targets))
			for _, t := range targets {
				tviews = append(tviews, targetView{
					URL:         t.URL.String(),
					Healthy:     t.Healthy(),
					ActiveConns: t.ActiveConns(),
				})
			}
			views = append(views, upstreamView{Name: name, BreakerState: up.BreakerState(), Targets: tviews})
		}
		sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
		writeJSON(w, views)
	}
}

func handleReload(reload func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := reload(); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "error": err.Error()})
			return
		}
		writeJSON(w, map[string]string{"status": "reloaded"})
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
