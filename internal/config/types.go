// Package config defines gatekeeper's YAML configuration schema, loading and
// validation.
package config

// Config is the root of gatekeeper's configuration file.
type Config struct {
	Server    ServerConfig              `yaml:"server"`
	Admin     AdminConfig               `yaml:"admin"`
	Upstreams map[string]UpstreamConfig `yaml:"upstreams"`
	Routes    []RouteConfig             `yaml:"routes"`
	Auth      AuthConfig                `yaml:"auth"`
	CORS      CORSConfig                `yaml:"cors"`
	Redis     RedisConfig               `yaml:"redis"`
}

// RedisConfig is only required if some route's rate_limit.backend is
// "redis" - it's not in the TZ's example config snippet since that's a
// deployment detail, not an algorithm choice.
type RedisConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

type ServerConfig struct {
	Listen            string   `yaml:"listen"`
	ReadHeaderTimeout Duration `yaml:"read_header_timeout"`
	MaxBodyBytes      ByteSize `yaml:"max_body_bytes"`
}

type AdminConfig struct {
	Listen   string `yaml:"listen"`
	TokenEnv string `yaml:"token_env"`
}

type Target struct {
	URL    string `yaml:"url"`
	Weight int    `yaml:"weight"`
}

type HealthCheckConfig struct {
	Path               string   `yaml:"path"`
	Interval           Duration `yaml:"interval"`
	Timeout            Duration `yaml:"timeout"`
	HealthyThreshold   int      `yaml:"healthy_threshold"`
	UnhealthyThreshold int      `yaml:"unhealthy_threshold"`
}

type CircuitBreakerConfig struct {
	FailureRatio float64  `yaml:"failure_ratio"`
	MinRequests  int      `yaml:"min_requests"`
	Window       Duration `yaml:"window"`
	OpenTimeout  Duration `yaml:"open_timeout"`
	HalfOpenMax  int      `yaml:"half_open_max"`
}

type RetriesConfig struct {
	Max            int     `yaml:"max"`
	OnlyIdempotent bool    `yaml:"only_idempotent"`
	BudgetRatio    float64 `yaml:"budget_ratio"`
}

type UpstreamConfig struct {
	Balancer       string               `yaml:"balancer"`
	Targets        []Target             `yaml:"targets"`
	HealthCheck    HealthCheckConfig    `yaml:"health_check"`
	CircuitBreaker CircuitBreakerConfig `yaml:"circuit_breaker"`
	Timeout        Duration             `yaml:"timeout"`
	Retries        RetriesConfig        `yaml:"retries"`
}

// MatchConfig selects requests for a route. Host is optional (empty matches
// any host); PathPrefix is required.
type MatchConfig struct {
	Host       string   `yaml:"host"`
	PathPrefix string   `yaml:"path_prefix"`
	Methods    []string `yaml:"methods"`
}

type RateLimitConfig struct {
	Key     string  `yaml:"key"`
	RPS     float64 `yaml:"rps"`
	Burst   int     `yaml:"burst"`
	Backend string  `yaml:"backend"`
	// FailOpen only matters for backend: redis - it decides whether a
	// request is allowed (fail-open) or rejected (fail-closed, the zero
	// value/default) when Redis itself is unreachable. The TZ calls this
	// out as something "задаётся в конфиге" (config-driven), unlike most
	// of the other unwritten defaults in this project.
	FailOpen bool `yaml:"fail_open"`
}

type RouteConfig struct {
	Match       MatchConfig      `yaml:"match"`
	StripPrefix string           `yaml:"strip_prefix"`
	Upstream    string           `yaml:"upstream"`
	Auth        string           `yaml:"auth"`
	RateLimit   *RateLimitConfig `yaml:"rate_limit"`
}

type JWTConfig struct {
	JWKSURL  string   `yaml:"jwks_url"`
	Issuer   string   `yaml:"issuer"`
	Audience string   `yaml:"audience"`
	CacheTTL Duration `yaml:"cache_ttl"`
}

type AuthConfig struct {
	APIKeysFile string    `yaml:"api_keys_file"`
	JWT         JWTConfig `yaml:"jwt"`
}

type CORSConfig struct {
	AllowedOrigins []string `yaml:"allowed_origins"`
}

// Enum values allowed in the config. Kept here so validate.go and tests
// share a single source of truth.
const (
	BalancerRoundRobin         = "round_robin"
	BalancerWeightedRoundRobin = "weighted_round_robin"
	BalancerLeastConn          = "least_conn"

	AuthNone   = "none"
	AuthAPIKey = "api_key"
	AuthJWT    = "jwt"

	RateLimitKeyIP        = "ip"
	RateLimitKeyAPIKey    = "api_key"
	RateLimitKeyJWTSub    = "jwt_sub"
	RateLimitBackendLocal = "local"
	RateLimitBackendRedis = "redis"
)
