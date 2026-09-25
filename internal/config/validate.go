package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

var validMethods = map[string]bool{
	"GET": true, "HEAD": true, "POST": true, "PUT": true,
	"DELETE": true, "OPTIONS": true, "PATCH": true,
}

// validator accumulates field-scoped errors instead of failing on the first
// one, so a misconfigured file reports every problem at once.
type validator struct {
	errs []error
}

func (v *validator) addf(field, format string, args ...any) {
	v.errs = append(v.errs, fmt.Errorf("%s: %s", field, fmt.Sprintf(format, args...)))
}

func (v *validator) require(cond bool, field, format string, args ...any) {
	if !cond {
		v.addf(field, format, args...)
	}
}

func (v *validator) err() error {
	if len(v.errs) == 0 {
		return nil
	}
	return errors.Join(v.errs...)
}

// Validate checks that cfg is complete and internally consistent, e.g. that
// routes reference existing upstreams and enum fields use known values.
// Every problem found is reported; errors.Is/As can inspect the joined
// result.
func Validate(cfg *Config) error {
	v := &validator{}

	validateServer(v, cfg.Server)
	validateAdmin(v, cfg)

	if len(cfg.Upstreams) == 0 {
		v.addf("upstreams", "at least one upstream is required")
	}
	for name, up := range cfg.Upstreams {
		validateUpstream(v, fmt.Sprintf("upstreams[%s]", name), up)
	}

	if len(cfg.Routes) == 0 {
		v.addf("routes", "at least one route is required")
	}
	needsAPIKeyAuth := false
	needsJWTAuth := false
	for i, route := range cfg.Routes {
		field := fmt.Sprintf("routes[%d]", i)
		needsAPIKey, needsJWT := validateRoute(v, field, route, cfg.Upstreams)
		needsAPIKeyAuth = needsAPIKeyAuth || needsAPIKey
		needsJWTAuth = needsJWTAuth || needsJWT
	}

	validateAuth(v, cfg.Auth, needsAPIKeyAuth, needsJWTAuth)
	validateCORS(v, cfg.CORS)

	return v.err()
}

func validateServer(v *validator, s ServerConfig) {
	validateListenAddr(v, "server.listen", s.Listen)
	v.require(s.ReadHeaderTimeout.Duration() > 0, "server.read_header_timeout", "must be > 0")
	v.require(s.MaxBodyBytes.Int64() > 0, "server.max_body_bytes", "must be > 0")
}

func validateAdmin(v *validator, cfg *Config) {
	validateListenAddr(v, "admin.listen", cfg.Admin.Listen)
	v.require(cfg.Admin.TokenEnv != "", "admin.token_env", "must be set (name of the env var holding the admin token)")
	if cfg.Admin.Listen != "" && cfg.Admin.Listen == cfg.Server.Listen {
		v.addf("admin.listen", "must differ from server.listen")
	}
}

func validateListenAddr(v *validator, field, addr string) {
	if addr == "" {
		v.addf(field, "must be set")
		return
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		v.addf(field, "invalid listen address %q: %s", addr, err)
		return
	}
	if port == "" {
		v.addf(field, "listen address %q must include a port", addr)
	}
}

func validateUpstream(v *validator, field string, up UpstreamConfig) {
	switch up.Balancer {
	case BalancerRoundRobin, BalancerWeightedRoundRobin, BalancerLeastConn:
	default:
		v.addf(field+".balancer", "unknown balancer %q", up.Balancer)
	}

	if len(up.Targets) == 0 {
		v.addf(field+".targets", "at least one target is required")
	}
	for i, t := range up.Targets {
		tf := fmt.Sprintf("%s.targets[%d]", field, i)
		u, err := url.Parse(t.URL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			v.addf(tf+".url", "must be an absolute http(s) URL, got %q", t.URL)
		}
		v.require(t.Weight >= 1, tf+".weight", "must be >= 1, got %d", t.Weight)
	}

	hc := up.HealthCheck
	v.require(strings.HasPrefix(hc.Path, "/"), field+".health_check.path", "must start with \"/\", got %q", hc.Path)
	v.require(hc.Interval.Duration() > 0, field+".health_check.interval", "must be > 0")
	v.require(hc.Timeout.Duration() > 0, field+".health_check.timeout", "must be > 0")
	v.require(hc.HealthyThreshold > 0, field+".health_check.healthy_threshold", "must be > 0")
	v.require(hc.UnhealthyThreshold > 0, field+".health_check.unhealthy_threshold", "must be > 0")

	cb := up.CircuitBreaker
	v.require(cb.FailureRatio > 0 && cb.FailureRatio <= 1, field+".circuit_breaker.failure_ratio", "must be in (0, 1], got %v", cb.FailureRatio)
	v.require(cb.MinRequests > 0, field+".circuit_breaker.min_requests", "must be > 0")
	v.require(cb.Window.Duration() > 0, field+".circuit_breaker.window", "must be > 0")
	v.require(cb.OpenTimeout.Duration() > 0, field+".circuit_breaker.open_timeout", "must be > 0")
	v.require(cb.HalfOpenMax > 0, field+".circuit_breaker.half_open_max", "must be > 0")

	v.require(up.Timeout.Duration() > 0, field+".timeout", "must be > 0")

	v.require(up.Retries.Max >= 0, field+".retries.max", "must be >= 0")
	v.require(up.Retries.BudgetRatio >= 0 && up.Retries.BudgetRatio <= 1, field+".retries.budget_ratio", "must be in [0, 1], got %v", up.Retries.BudgetRatio)
}

// validateRoute reports whether the route needs auth.api_keys_file /
// auth.jwt to be configured, so the caller can cross-check that once all
// routes have been seen.
func validateRoute(v *validator, field string, r RouteConfig, upstreams map[string]UpstreamConfig) (needsAPIKey, needsJWT bool) {
	v.require(strings.HasPrefix(r.Match.PathPrefix, "/"), field+".match.path_prefix", "must start with \"/\", got %q", r.Match.PathPrefix)
	for i, m := range r.Match.Methods {
		mu := strings.ToUpper(m)
		if !validMethods[mu] {
			v.addf(fmt.Sprintf("%s.match.methods[%d]", field, i), "unknown HTTP method %q", m)
		}
	}
	if r.StripPrefix != "" {
		v.require(strings.HasPrefix(r.StripPrefix, "/"), field+".strip_prefix", "must start with \"/\", got %q", r.StripPrefix)
	}

	if r.Upstream == "" {
		v.addf(field+".upstream", "must be set")
	} else if _, ok := upstreams[r.Upstream]; !ok {
		v.addf(field+".upstream", "references unknown upstream %q", r.Upstream)
	}

	switch r.Auth {
	case AuthNone:
	case AuthAPIKey:
		needsAPIKey = true
	case AuthJWT:
		needsJWT = true
	default:
		v.addf(field+".auth", "unknown auth mode %q (want none|api_key|jwt)", r.Auth)
	}

	if r.RateLimit != nil {
		rl := r.RateLimit
		rf := field + ".rate_limit"
		switch rl.Key {
		case RateLimitKeyIP, RateLimitKeyAPIKey, RateLimitKeyJWTSub:
		default:
			v.addf(rf+".key", "unknown rate limit key %q (want ip|api_key|jwt_sub)", rl.Key)
		}
		v.require(rl.RPS > 0, rf+".rps", "must be > 0")
		v.require(rl.Burst > 0, rf+".burst", "must be > 0")
		switch rl.Backend {
		case RateLimitBackendLocal, RateLimitBackendRedis:
		default:
			v.addf(rf+".backend", "unknown rate limit backend %q (want local|redis)", rl.Backend)
		}
	}

	return needsAPIKey, needsJWT
}

func validateAuth(v *validator, a AuthConfig, needsAPIKey, needsJWT bool) {
	if needsAPIKey {
		v.require(a.APIKeysFile != "", "auth.api_keys_file", "must be set: at least one route uses auth: api_key")
	}
	if needsJWT {
		jf := "auth.jwt"
		if a.JWT.JWKSURL == "" {
			v.addf(jf+".jwks_url", "must be set: at least one route uses auth: jwt")
		} else if u, err := url.Parse(a.JWT.JWKSURL); err != nil || u.Scheme == "" || u.Host == "" {
			v.addf(jf+".jwks_url", "must be an absolute http(s) URL, got %q", a.JWT.JWKSURL)
		}
		v.require(a.JWT.Issuer != "", jf+".issuer", "must be set: at least one route uses auth: jwt")
		v.require(a.JWT.Audience != "", jf+".audience", "must be set: at least one route uses auth: jwt")
		v.require(a.JWT.CacheTTL.Duration() > 0, jf+".cache_ttl", "must be > 0")
	}
}

func validateCORS(v *validator, c CORSConfig) {
	for i, origin := range c.AllowedOrigins {
		field := fmt.Sprintf("cors.allowed_origins[%d]", i)
		u, err := url.Parse(origin)
		if err != nil || u.Scheme == "" || u.Host == "" || u.Path != "" {
			v.addf(field, "must be a bare origin like \"http://host:port\", got %q", origin)
		}
	}
}
