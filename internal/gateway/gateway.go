// Package gateway wires config, the router and balancers into the actual
// request-handling http.Handler: match a route, pick a target, proxy the
// request.
package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"sync/atomic"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"gatekeeper/internal/auth"
	"gatekeeper/internal/balancer"
	"gatekeeper/internal/breaker"
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
	breaker       *breaker.CircuitBreaker
	retryBudget   *breaker.RetryBudget
	retries       config.RetriesConfig
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

	// Also Pass 0: build the (at most one each) API key store / JWT
	// verifier this config needs, before anything starts a goroutine.
	// Neither actually starts a goroutine itself (the JWKS client
	// refreshes lazily on demand), but loading api_keys_file can fail and
	// should do so before health checkers start, same reasoning as above.
	var needsAPIKey, needsJWT bool
	for _, r := range cfg.Routes {
		switch r.Auth {
		case config.AuthAPIKey:
			needsAPIKey = true
		case config.AuthJWT:
			needsJWT = true
		}
	}
	var apiKeyStore *auth.APIKeyStore
	if needsAPIKey {
		var err error
		apiKeyStore, err = auth.LoadAPIKeys(cfg.Auth.APIKeysFile)
		if err != nil {
			return nil, fmt.Errorf("loading api keys: %w", err)
		}
	}
	var jwtVerifier *auth.JWTVerifier
	if needsJWT {
		jwks := auth.NewJWKSClient(cfg.Auth.JWT.JWKSURL, cfg.Auth.JWT.CacheTTL.Duration())
		jwtVerifier = auth.NewJWTVerifier(jwks, cfg.Auth.JWT.Issuer, cfg.Auth.JWT.Audience)
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

		cb := breaker.New(breaker.RealClock{}, breaker.Config{
			FailureRatio: upCfg.CircuitBreaker.FailureRatio,
			MinRequests:  upCfg.CircuitBreaker.MinRequests,
			Window:       upCfg.CircuitBreaker.Window.Duration(),
			OpenTimeout:  upCfg.CircuitBreaker.OpenTimeout.Duration(),
			HalfOpenMax:  upCfg.CircuitBreaker.HalfOpenMax,
		})
		retryBudget := breaker.NewRetryBudget(breaker.RealClock{}, upCfg.Retries.BudgetRatio)

		up := &Upstream{
			Name:          name,
			Balancer:      bal,
			Timeout:       upCfg.Timeout.Duration(),
			healthChecker: checker,
			breaker:       cb,
			retryBudget:   retryBudget,
			retries:       upCfg.Retries,
		}
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
		authenticate(apiKeyStore, jwtVerifier),
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
// bounds the request to the upstream's configured timeout, and proxies
// it - retrying on another target if the route's retry policy allows it.
//
// Retrying safely requires not having already sent bytes to the real
// client, so this only buffers a whole attempt's response (via
// bufferedResponse) when retries are actually possible for this request;
// otherwise it streams straight through exactly as before, at no extra
// allocation/latency cost - see retry.go.
func proxyHandler(w http.ResponseWriter, r *http.Request) {
	up := r.Context().Value(upstreamContextKey{}).(*Upstream)
	up.retryBudget.RecordRequest()

	canRetry := up.retries.Max > 0 && (!up.retries.OnlyIdempotent || isIdempotentMethod(r.Method))
	if !canRetry {
		proxyOnce(w, up, r)
		return
	}

	var bodyBytes []byte
	if r.Body != nil && r.Body != http.NoBody {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}
		_ = r.Body.Close()
		bodyBytes = b
	}

	var rec *bufferedResponse
	for attempt := 0; attempt <= up.retries.Max; attempt++ {
		if !up.breaker.Allow() {
			http.Error(w, "circuit breaker open", http.StatusServiceUnavailable)
			return
		}
		target, release, err := up.Balancer.Pick()
		if err != nil {
			http.Error(w, "no healthy upstream targets", http.StatusServiceUnavailable)
			return
		}
		if fields := reqctx.AccessFieldsFrom(r.Context()); fields != nil {
			fields.Target = target.URL.String()
		}
		if bodyBytes != nil {
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		rec = newBufferedResponse()
		ctx, cancel := context.WithTimeout(r.Context(), up.Timeout)
		ctx = context.WithValue(ctx, targetContextKey{}, target)
		up.proxy.ServeHTTP(rec, r.WithContext(ctx))
		cancel()
		release()

		retryable := isRetryableStatus(rec.status)
		up.breaker.RecordResult(!retryable)
		if !retryable || attempt == up.retries.Max || !up.retryBudget.AllowRetry() {
			break
		}
	}
	rec.copyTo(w)
}

// proxyOnce is the no-retry fast path: single attempt, no body buffering.
func proxyOnce(w http.ResponseWriter, up *Upstream, r *http.Request) {
	if !up.breaker.Allow() {
		http.Error(w, "circuit breaker open", http.StatusServiceUnavailable)
		return
	}

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

	rw := &statusRecordingWriter{ResponseWriter: w}
	up.proxy.ServeHTTP(rw, r.WithContext(ctx))
	up.breaker.RecordResult(!isRetryableStatus(rw.status))
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
