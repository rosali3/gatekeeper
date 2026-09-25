// Package router matches incoming requests (host, path, method) to a
// configured route. It knows nothing about upstreams or proxying - just
// which route, if any, a request belongs to.
package router

import (
	"net"
	"strings"

	"gatekeeper/internal/config"
)

// Route is a config.RouteConfig compiled into a form cheap to match against
// on every request.
type Route struct {
	Host        string // "" matches any host
	PathPrefix  string
	Methods     map[string]struct{} // empty/nil means "any method"
	StripPrefix string
	Upstream    string
	Auth        string // "none" | "api_key" | "jwt"
}

func (r *Route) allowsMethod(method string) bool {
	if len(r.Methods) == 0 {
		return true
	}
	_, ok := r.Methods[strings.ToUpper(method)]
	return ok
}

// Table holds the compiled routes for one config generation.
type Table struct {
	routes []*Route
}

// Routes returns the compiled routes in the same order as the
// []config.RouteConfig passed to Build, so callers that need to associate
// per-route runtime state (e.g. a rate limiter) with a *Route can zip the
// two slices together by index.
func (t *Table) Routes() []*Route { return t.routes }

// Build compiles routes from config in the order given. config.Validate is
// assumed to have already checked upstream references and enum values.
func Build(routes []config.RouteConfig) *Table {
	compiled := make([]*Route, 0, len(routes))
	for _, r := range routes {
		var methods map[string]struct{}
		if len(r.Match.Methods) > 0 {
			methods = make(map[string]struct{}, len(r.Match.Methods))
			for _, m := range r.Match.Methods {
				methods[strings.ToUpper(m)] = struct{}{}
			}
		}
		compiled = append(compiled, &Route{
			Host:        normalizeHost(r.Match.Host),
			PathPrefix:  r.Match.PathPrefix,
			Methods:     methods,
			StripPrefix: r.StripPrefix,
			Upstream:    r.Upstream,
			Auth:        r.Auth,
		})
	}
	return &Table{routes: compiled}
}

// Match finds the route for (host, path, method). Among routes matching
// host and method, the one with the longest PathPrefix wins; ties keep
// whichever was declared first in the config.
func (t *Table) Match(host, path, method string) (*Route, bool) {
	host = normalizeHost(host)

	var best *Route
	for _, r := range t.routes {
		if r.Host != "" && r.Host != host {
			continue
		}
		if !r.allowsMethod(method) {
			continue
		}
		if !strings.HasPrefix(path, r.PathPrefix) {
			continue
		}
		if best == nil || len(r.PathPrefix) > len(best.PathPrefix) {
			best = r
		}
	}
	if best == nil {
		return nil, false
	}
	return best, true
}

// StripPath applies the route's strip_prefix to path, keeping a leading
// slash so the result is always a valid path.
func (r *Route) StripPath(path string) string {
	if r.StripPrefix == "" {
		return path
	}
	trimmed := strings.TrimPrefix(path, r.StripPrefix)
	if trimmed == "" || trimmed[0] != '/' {
		trimmed = "/" + trimmed
	}
	return trimmed
}

// normalizeHost strips a port (IPv6-safe) and lower-cases the host, so
// "Example.com:8080" and "example.com" match the same route.
func normalizeHost(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(host)
}
