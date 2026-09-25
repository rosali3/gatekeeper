// Package gateway wires config, the router and balancers into the actual
// request-handling http.Handler: match a route, pick a target, proxy the
// request.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"sync/atomic"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"gatekeeper/internal/balancer"
	"gatekeeper/internal/config"
	"gatekeeper/internal/health"
	"gatekeeper/internal/middleware"
	"gatekeeper/internal/reqctx"
	"gatekeeper/internal/router"
)

// upstreamContextKey carries the resolved *Upstream from route matching to
// the proxy handler.
type upstreamContextKey struct{}

// Upstream is one configured upstream's runtime state: how to pick a
// target and how long to wait for it.
type Upstream struct {
	Name          string
	Balancer      balancer.Balancer
	Timeout       time.Duration
	proxy         *httputil.ReverseProxy
	healthChecker *health.Checker
}

// Snapshot is one immutable generation of everything needed to handle a
// request. A config reload (stage 7) builds a new Snapshot and swaps it in
// atomically via Gateway.Swap, so in-flight requests keep using the old one.
type Snapshot struct {
	Router    *router.Table
	Upstreams map[string]*Upstream
	Handler   http.Handler
}

// Build compiles cfg into a Snapshot, including the fully assembled
// middleware chain, and starts one health-check goroutine per upstream
// bound to ctx - canceling ctx (e.g. on shutdown) stops them.
// cfg is assumed already validated (config.Validate).
func Build(ctx context.Context, cfg *config.Config, log *slog.Logger) (*Snapshot, error) {
	transport := newTransport()

	// Pass 0: validate every route's rate_limit config before anything
	// else starts a goroutine (health checkers below, rate-limit janitors
	// later) - otherwise a bad route discovered late could leak whatever
	// was already started for routes/upstreams processed before it.
	if err := validateRateLimitRoutes(cfg.Routes); err != nil {
		return nil, err
	}

	// Pass 1: build every balancer before starting anything. If any
	// upstream is misconfigured, Build returns without ever having
	// started a health-check goroutine - otherwise a failure partway
	// through map iteration could leak the checkers already started for
	// upstreams processed before it.
	balancers := make(map[string]balancer.Balancer, len(cfg.Upstreams))
	var errs []error
	for name, upCfg := range cfg.Upstreams {
		specs := make([]balancer.TargetSpec, len(upCfg.Targets))
		for i, t := range upCfg.Targets {
			specs[i] = balancer.TargetSpec{URL: t.URL, Weight: t.Weight}
		}
		bal, err := balancer.New(upCfg.Balancer, specs)
		if err != nil {
			errs = append(errs, fmt.Errorf("upstream %q: %w", name, err))
			continue
		}
		balancers[name] = bal
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	// Pass 2: every balancer is valid, so it's now safe to start a
	// health-check goroutine per upstream.
	upstreams := make(map[string]*Upstream, len(cfg.Upstreams))
	for name, upCfg := range cfg.Upstreams {
		bal := balancers[name]

		healthCfg := health.Config{
			Path:               upCfg.HealthCheck.Path,
			Interval:           upCfg.HealthCheck.Interval.Duration(),
			Timeout:            upCfg.HealthCheck.Timeout.Duration(),
			HealthyThreshold:   upCfg.HealthCheck.HealthyThreshold,
			UnhealthyThreshold: upCfg.HealthCheck.UnhealthyThreshold,
		}
		checker := health.NewChecker(name, healthCfg, bal.Targets(), log)
		go checker.Run(ctx)

		up := &Upstream{Name: name, Balancer: bal, Timeout: upCfg.Timeout.Duration(), healthChecker: checker}
		up.proxy = newReverseProxy(name, transport, log, checker)
		upstreams[name] = up
	}

	routerTable := router.Build(cfg.Routes)
	snap := &Snapshot{
		Router:    routerTable,
		Upstreams: upstreams,
	}

	// The redis client is only actually created if some route's rate_limit
	// uses backend: redis (config.Validate already guarantees cfg.Redis.Addr
	// is set in that case). It's shared across every redis-backed route's
	// limiter - go-redis clients pool their own connections and are safe
	// for concurrent use.
	var redisClient goredis.UniversalClient
	getRedisClient := func() goredis.UniversalClient {
		if redisClient == nil {
			redisClient = goredis.NewClient(&goredis.Options{
				Addr:     cfg.Redis.Addr,
				Password: cfg.Redis.Password,
				DB:       cfg.Redis.DB,
			})
		}
		return redisClient
	}
	rateLimiters := buildRateLimiters(ctx, cfg.Routes, routerTable.Routes(), getRedisClient)

	corsOrigins := make(map[string]struct{}, len(cfg.CORS.AllowedOrigins))
	for _, o := range cfg.CORS.AllowedOrigins {
		corsOrigins[o] = struct{}{}
	}

	snap.Handler = middleware.Chain(
		http.HandlerFunc(proxyHandler),
		middleware.Recover(log),
		middleware.RequestID(),
		middleware.AccessLog(log),
		middleware.CORS(corsOrigins),
		middleware.BodyLimit(cfg.Server.MaxBodyBytes.Int64()),
		routeMatch(snap),
		rateLimit(rateLimiters),
	)

	return snap, nil
}

// routeMatch finds the route for the request, records it for the access
// log, strips its prefix, and hands off to the proxy via context. A 404 is
// returned directly (no downstream middleware runs) when nothing matches.
func routeMatch(snap *Snapshot) middleware.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			route, ok := snap.Router.Match(r.Host, r.URL.Path, r.Method)
			if !ok {
				http.NotFound(w, r)
				return
			}
			// Guaranteed present: config.Validate rejects routes that
			// reference an unknown upstream, and Build fills in every
			// upstream it accepted before compiling routes.
			up := snap.Upstreams[route.Upstream]

			if fields := reqctx.AccessFieldsFrom(r.Context()); fields != nil {
				fields.Route = route.PathPrefix
				fields.Upstream = up.Name
			}

			r.URL.Path = route.StripPath(r.URL.Path)
			ctx := context.WithValue(r.Context(), upstreamContextKey{}, up)
			ctx = context.WithValue(ctx, routeContextKey{}, route)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// proxyHandler picks a target for the upstream resolved by routeMatch,
// bounds the request to the upstream's configured timeout, and proxies it.
func proxyHandler(w http.ResponseWriter, r *http.Request) {
	up := r.Context().Value(upstreamContextKey{}).(*Upstream)

	target, release, err := up.Balancer.Pick()
	if err != nil {
		http.Error(w, "no healthy upstream targets", http.StatusServiceUnavailable)
		return
	}
	defer release()

	if fields := reqctx.AccessFieldsFrom(r.Context()); fields != nil {
		fields.Target = target.URL.String()
	}

	ctx, cancel := context.WithTimeout(r.Context(), up.Timeout)
	defer cancel()
	ctx = context.WithValue(ctx, targetContextKey{}, target)
	up.proxy.ServeHTTP(w, r.WithContext(ctx))
}

// Gateway is the gateway's top-level http.Handler. Its Snapshot can be
// swapped atomically, e.g. on config reload, without disrupting requests
// already in flight against the old one.
type Gateway struct {
	snap atomic.Pointer[Snapshot]
}

func New() *Gateway {
	return &Gateway{}
}

// Swap installs s as the snapshot used for all requests from now on.
func (g *Gateway) Swap(s *Snapshot) {
	g.snap.Store(s)
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	snap := g.snap.Load()
	if snap == nil {
		http.Error(w, "gateway not ready", http.StatusServiceUnavailable)
		return
	}
	snap.Handler.ServeHTTP(w, r)
}
