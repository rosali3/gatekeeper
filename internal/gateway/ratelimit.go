package gateway

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
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
// request it keys on.
type routeRateLimit struct {
	limiter ratelimit.Limiter
	keyType string
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
// the middleware can look it up in O(1). routes and compiled must be the
// same length and order (router.Table.Routes() guarantees this for a
// table built from routes). Callers must call validateRateLimitRoutes
// first - this assumes every route is buildable and starts goroutines
// unconditionally.
//
// Local limiters get a janitor goroutine bound to ctx, same lifecycle as
// health checkers. redisClient is built lazily (nil until the first
// redis-backed route needs it) and reused across routes/instances since
// go-redis's client is safe for concurrent use and pools connections
// itself.
func buildRateLimiters(ctx context.Context, routes []config.RouteConfig, compiled []*router.Route, getRedisClient func() goredis.UniversalClient) map[*router.Route]*routeRateLimit {
	limiters := make(map[*router.Route]*routeRateLimit, len(routes))
	for i, r := range routes {
		if r.RateLimit == nil {
			continue
		}
		rl := r.RateLimit

		cfg := ratelimit.Config{RPS: rl.RPS, Burst: rl.Burst}
		var limiter ratelimit.Limiter
		switch rl.Backend {
		case config.RateLimitBackendLocal:
			local := ratelimit.NewLocal(ratelimit.RealClock{}, cfg)
			go local.Run(ctx)
			limiter = local
		case config.RateLimitBackendRedis:
			limiter = ratelimit.NewRedis(getRedisClient(), ratelimit.RealClock{}, cfg, "ratelimit:", rl.FailOpen)
		}

		limiters[compiled[i]] = &routeRateLimit{limiter: limiter, keyType: rl.Key}
	}
	return limiters
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
