package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoad_Valid(t *testing.T) {
	cfg, err := Load("testdata/valid.yaml")
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}

	if got, want := cfg.Server.Listen, ":8080"; got != want {
		t.Errorf("Server.Listen = %q, want %q", got, want)
	}
	if got, want := cfg.Server.ReadHeaderTimeout.Duration(), 5*time.Second; got != want {
		t.Errorf("Server.ReadHeaderTimeout = %v, want %v", got, want)
	}
	if got, want := cfg.Server.MaxBodyBytes.Int64(), int64(10*1<<20); got != want {
		t.Errorf("Server.MaxBodyBytes = %d, want %d", got, want)
	}

	up, ok := cfg.Upstreams["floorplan"]
	if !ok {
		t.Fatalf("upstream %q not found", "floorplan")
	}
	if got, want := up.Balancer, BalancerLeastConn; got != want {
		t.Errorf("Upstream.Balancer = %q, want %q", got, want)
	}
	if got, want := len(up.Targets), 2; got != want {
		t.Errorf("len(Targets) = %d, want %d", got, want)
	}
	if got, want := up.HealthCheck.Interval.Duration(), 5*time.Second; got != want {
		t.Errorf("HealthCheck.Interval = %v, want %v", got, want)
	}
	if got, want := up.CircuitBreaker.FailureRatio, 0.5; got != want {
		t.Errorf("CircuitBreaker.FailureRatio = %v, want %v", got, want)
	}

	if got, want := len(cfg.Routes), 1; got != want {
		t.Fatalf("len(Routes) = %d, want %d", got, want)
	}
	route := cfg.Routes[0]
	if got, want := route.Match.PathPrefix, "/api/floorplan/"; got != want {
		t.Errorf("Route.Match.PathPrefix = %q, want %q", got, want)
	}
	if route.RateLimit == nil {
		t.Fatalf("Route.RateLimit = nil, want non-nil")
	}
	if got, want := route.RateLimit.RPS, 5.0; got != want {
		t.Errorf("RateLimit.RPS = %v, want %v", got, want)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("testdata/does-not-exist.yaml")
	if err == nil {
		t.Fatal("Load: expected error for missing file, got nil")
	}
}

func TestLoad_UnknownFieldRejected(t *testing.T) {
	_, err := Load("testdata/unknown_field.yaml")
	if err == nil {
		t.Fatal("Load: expected error for unknown field, got nil")
	}
	if !strings.Contains(err.Error(), "bogus_field") {
		t.Errorf("Load: error %q does not mention the unknown field", err)
	}
}

func TestDecode_InvalidDuration(t *testing.T) {
	_, err := decode(strings.NewReader(validYAMLWith(`read_header_timeout: "not-a-duration"`, "read_header_timeout: 5s")))
	if err == nil {
		t.Fatal("decode: expected error for invalid duration, got nil")
	}
}

func TestDecode_InvalidByteSize(t *testing.T) {
	_, err := decode(strings.NewReader(validYAMLWith(`max_body_bytes: "not-a-size"`, "max_body_bytes: 10MB")))
	if err == nil {
		t.Fatal("decode: expected error for invalid byte size, got nil")
	}
}

func TestByteSize_Units(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"10", 10},
		{"10B", 10},
		{"1KB", 1 << 10},
		{"10MB", 10 << 20},
		{"1GB", 1 << 30},
		{"1TB", 1 << 40},
	}
	for _, tt := range tests {
		got, err := parseByteSize(tt.in)
		if err != nil {
			t.Errorf("parseByteSize(%q): unexpected error: %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parseByteSize(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestByteSize_Invalid(t *testing.T) {
	for _, in := range []string{"", "MB", "-5MB", "ten"} {
		if _, err := parseByteSize(in); err == nil {
			t.Errorf("parseByteSize(%q): expected error, got nil", in)
		}
	}
}

func TestValidate_RouteReferencesUnknownUpstream(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Routes[0].Upstream = "does-not-exist"

	err := Validate(cfg)
	if err == nil {
		t.Fatal("Validate: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown upstream") {
		t.Errorf("Validate: error %q does not mention unknown upstream", err)
	}
}

func TestValidate_UnknownBalancer(t *testing.T) {
	cfg := minimalValidConfig()
	up := cfg.Upstreams["floorplan"]
	up.Balancer = "random"
	cfg.Upstreams["floorplan"] = up

	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "balancer") {
		t.Fatalf("Validate: expected balancer error, got %v", err)
	}
}

func TestValidate_TargetWeightMustBePositive(t *testing.T) {
	cfg := minimalValidConfig()
	up := cfg.Upstreams["floorplan"]
	up.Targets[0].Weight = 0
	cfg.Upstreams["floorplan"] = up

	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "weight") {
		t.Fatalf("Validate: expected weight error, got %v", err)
	}
}

func TestValidate_JWTRouteRequiresJWTConfig(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Routes[0].Auth = AuthJWT
	// cfg.Auth.JWT left zero-valued.

	err := Validate(cfg)
	if err == nil {
		t.Fatal("Validate: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "auth.jwt") {
		t.Errorf("Validate: error %q does not mention auth.jwt", err)
	}
}

func TestValidate_APIKeyRouteRequiresKeysFile(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Routes[0].Auth = AuthAPIKey
	cfg.Auth.APIKeysFile = ""

	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "api_keys_file") {
		t.Fatalf("Validate: expected api_keys_file error, got %v", err)
	}
}

func TestValidate_BadCORSOrigin(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.CORS.AllowedOrigins = []string{"not-a-url"}

	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "cors.allowed_origins") {
		t.Fatalf("Validate: expected CORS error, got %v", err)
	}
}

func TestValidate_ValidConfigHasNoError(t *testing.T) {
	cfg := minimalValidConfig()
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate: unexpected error: %v", err)
	}
}

func TestValidate_ReportsMultipleErrors(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Server.Listen = ""
	cfg.Admin.Listen = ""

	err := Validate(cfg)
	if err == nil {
		t.Fatal("Validate: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "server.listen") || !strings.Contains(err.Error(), "admin.listen") {
		t.Errorf("Validate: expected both server.listen and admin.listen errors, got %v", err)
	}
}

func TestValidate_RedisBackendRouteRequiresRedisAddr(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Routes[0].RateLimit = &RateLimitConfig{
		Key: RateLimitKeyIP, RPS: 5, Burst: 10, Backend: RateLimitBackendRedis,
	}

	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "redis.addr") {
		t.Fatalf("Validate: expected a redis.addr error, got %v", err)
	}

	cfg.Redis.Addr = "localhost:6379"
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate: unexpected error once redis.addr is set: %v", err)
	}
}

// minimalValidConfig returns a config equivalent to testdata/valid.yaml but
// without the redis rate-limit/JWT wiring, so tests can flip one field at a
// time.
func minimalValidConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Listen:            ":8080",
			ReadHeaderTimeout: Duration(5 * time.Second),
			MaxBodyBytes:      ByteSize(10 << 20),
		},
		Admin: AdminConfig{
			Listen:   ":9090",
			TokenEnv: "ADMIN_TOKEN",
		},
		Upstreams: map[string]UpstreamConfig{
			"floorplan": {
				Balancer: BalancerLeastConn,
				Targets: []Target{
					{URL: "http://floorplan-api-1:8080", Weight: 1},
				},
				HealthCheck: HealthCheckConfig{
					Path:               "/healthz",
					Interval:           Duration(5 * time.Second),
					Timeout:            Duration(time.Second),
					HealthyThreshold:   2,
					UnhealthyThreshold: 3,
				},
				CircuitBreaker: CircuitBreakerConfig{
					FailureRatio: 0.5,
					MinRequests:  20,
					Window:       Duration(10 * time.Second),
					OpenTimeout:  Duration(15 * time.Second),
					HalfOpenMax:  3,
				},
				Timeout: Duration(30 * time.Second),
				Retries: RetriesConfig{Max: 2, OnlyIdempotent: true, BudgetRatio: 0.1},
			},
		},
		Routes: []RouteConfig{
			{
				Match:    MatchConfig{PathPrefix: "/api/floorplan/", Methods: []string{"GET"}},
				Upstream: "floorplan",
				Auth:     AuthNone,
			},
		},
	}
}

// validYAMLWith reads testdata/valid.yaml and replaces `old` with `new`,
// letting a test corrupt exactly one field while keeping the rest valid.
func validYAMLWith(new, old string) string {
	data, err := os.ReadFile("testdata/valid.yaml")
	if err != nil {
		panic(err)
	}
	return strings.Replace(string(data), old, new, 1)
}
