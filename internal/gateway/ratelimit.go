package gateway

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"reflect"
	"strconv"

	goredis "github.com/redis/go-redis/v9"

	"gatekeeper/internal/config"
	"gatekeeper/internal/middleware"
	"gatekeeper/internal/ratelimit"
	"gatekeeper/internal/router"
)

// routeContextKey carries the matched *router.Route from routeMatch to the
// rate-limit middleware, which needs to know which route's limiter to use.
type routeContextKey struct{}

// routeRateLimit is one route's configured limiter plus which part of the
// request it keys on. stopJanitor cancels a local limiter's janitor
// goroutine (nil for a redis-backed limiter, which has none) - called by
// Build on reload for any entry that isn't carried over into the new
// Snapshot.
type routeRateLimit struct {
	limiter     ratelimit.Limiter
	keyType     string
	stopJanitor context.CancelFunc
}

// validateRateLimitRoutes checks every route's rate_limit config can
// actually be built, without starting anything. Build calls this before
// starting any goroutine (health checkers or rate-limit janitors) so a
// bad route config can't leak goroutines already started for routes/
// upstreams processed before it.
func validateRateLimitRoutes(routes []config.RouteConfig) error {
	var errs []error
	for i, r := range routes {
		if r.RateLimit != nil && r.RateLimit.Key == config.RateLimitKeyJWTSub {
			errs = append(errs, fmt.Errorf("routes[%d].rate_limit.key: %q requires JWT auth, not implemented yet", i, r.RateLimit.Key))
		}
	}
	return errors.Join(errs...)
}

// buildRateLimiters constructs one ratelimit.Limiter per route that
// configures rate_limit, keyed by the route's compiled *router.Route so
// the middleware can look it up in O(1), and also returns them as a
// slice in route order (for the next reload to diff against). routes and
// compiled must be the same length and order (router.Table.Routes()
// guarantees this for a table built from routes). Callers must call
// validateRateLimitRoutes first - this assumes every route is buildable.
//
// prevRouteConfigs/prevRouteLimiters are the previous Snapshot's
// equivalents (nil on first load). Routes are matched across reloads by
// index - config.RouteConfig has no stable id/name field, so reordering
// routes in the config defeats reuse for the reordered ones (they'll just
// be rebuilt fresh, not a correctness problem, just missed reuse). A
// route whose rate_limit config is byte-for-byte unchanged reuses its
// existing limiter (bucket state and all); anything not reused has its
// janitor, if any, stopped so repeated reloads can't accumulate them.
//
// Local limiters get a janitor goroutine bound to ctx, same lifecycle as
// health checkers. redisClient is built lazily (nil until the first
// redis-backed route needs it) and reused across routes/instances since
// go-redis's client is safe for concurrent use and pools connections
// itself.
func buildRateLimiters(
	ctx context.Context,
	routes []config.RouteConfig,
	compiled []*router.Route,
	getRedisClient func() goredis.UniversalClient,
	prevRouteConfigs []config.RouteConfig,
	prevRouteLimiters []*routeRateLimit,
) (map[*router.Route]*routeRateLimit, []*routeRateLimit) {
	limiters := make(map[*router.Route]*routeRateLimit, len(routes))
	byIndex := make([]*routeRateLimit, len(routes))
	reused := make([]bool, len(prevRouteLimiters))

	for i, r := range routes {
		if r.RateLimit == nil {
			continue
		}

		if i < len(prevRouteConfigs) && i < len(prevRouteLimiters) && prevRouteLimiters[i] != nil &&
			reflect.DeepEqual(prevRouteConfigs[i], r) {
			byIndex[i] = prevRouteLimiters[i]
			limiters[compiled[i]] = prevRouteLimiters[i]
			reused[i] = true
			continue
		}

		rl := r.RateLimit
		cfg := ratelimit.Config{RPS: rl.RPS, Burst: rl.Burst}
		var limiter ratelimit.Limiter
		var stopJanitor context.CancelFunc
		switch rl.Backend {
		case config.RateLimitBackendLocal:
			local := ratelimit.NewLocal(ratelimit.RealClock{}, cfg)
			janitorCtx, cancel := context.WithCancel(ctx)
			go local.Run(janitorCtx)
			limiter = local
			stopJanitor = cancel
		case config.RateLimitBackendRedis:
			limiter = ratelimit.NewRedis(getRedisClient(), ratelimit.RealClock{}, cfg, "ratelimit:", rl.FailOpen)
		}

		rrl := &routeRateLimit{limiter: limiter, keyType: rl.Key, stopJanitor: stopJanitor}
		byIndex[i] = rrl
		limiters[compiled[i]] = rrl
	}

	for i, old := range prevRouteLimiters {
		if old != nil && !reused[i] && old.stopJanitor != nil {
			old.stopJanitor()
		}
	}

	return limiters, byIndex
}

// rateLimit enforces the per-route limiter set up by buildRateLimiters. A
// route with no rate_limit configured has no entry in limiters and passes
// straight through.
func rateLimit(limiters map[*router.Route]*routeRateLimit) middleware.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			route, _ := r.Context().Value(routeContextKey{}).(*router.Route)
			rl, ok := limiters[route]
			if !ok {
				next.ServeHTTP(w, r)
				return
			}

			key := rateLimitKey(rl.keyType, r)
			result, err := rl.limiter.Allow(r.Context(), key)
			if err != nil {
				http.Error(w, "rate limiting unavailable", http.StatusServiceUnavailable)
				return
			}

			w.Header().Set("RateLimit-Limit", strconv.Itoa(result.Limit))
			w.Header().Set("RateLimit-Remaining", strconv.Itoa(result.Remaining))
			w.Header().Set("RateLimit-Reset", strconv.Itoa(int(math.Ceil(result.ResetIn.Seconds()))))
			if !result.Allowed {
				w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(result.RetryAfter.Seconds()))))
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// rateLimitKey extracts the bucketing key for a request. api_key requests
// with no X-API-Key header all share one bucket rather than erroring -
// auth (stage 5) is what actually rejects a missing/invalid key; rate
// limiting here just needs some key to bucket on.
func rateLimitKey(keyType string, r *http.Request) string {
	if keyType == config.RateLimitKeyAPIKey {
		return r.Header.Get("X-API-Key")
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
