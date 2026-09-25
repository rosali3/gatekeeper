package router

import (
	"testing"

	"gatekeeper/internal/config"
)

func TestRoutes_SameOrderAsInput(t *testing.T) {
	table := Build([]config.RouteConfig{
		{Match: config.MatchConfig{PathPrefix: "/a/"}, Upstream: "a"},
		{Match: config.MatchConfig{PathPrefix: "/b/"}, Upstream: "b"},
	})

	routes := table.Routes()
	if len(routes) != 2 || routes[0].Upstream != "a" || routes[1].Upstream != "b" {
		t.Fatalf("Routes() = %v, want [a, b] in input order", routes)
	}
}

func TestBuild_CompilesAuthMode(t *testing.T) {
	table := Build([]config.RouteConfig{
		{Match: config.MatchConfig{PathPrefix: "/a/"}, Upstream: "a", Auth: config.AuthJWT},
	})
	if got := table.Routes()[0].Auth; got != config.AuthJWT {
		t.Errorf("Auth = %q, want %q", got, config.AuthJWT)
	}
}

func TestMatch_LongestPrefixWins(t *testing.T) {
	table := Build([]config.RouteConfig{
		{Match: config.MatchConfig{PathPrefix: "/api/"}, Upstream: "generic"},
		{Match: config.MatchConfig{PathPrefix: "/api/floorplan/"}, Upstream: "floorplan"},
	})

	route, ok := table.Match("gateway.local", "/api/floorplan/render", "GET")
	if !ok {
		t.Fatal("Match: expected a match, got none")
	}
	if route.Upstream != "floorplan" {
		t.Errorf("Upstream = %q, want %q (longest prefix should win)", route.Upstream, "floorplan")
	}
}

func TestMatch_NoMatchingPrefix(t *testing.T) {
	table := Build([]config.RouteConfig{
		{Match: config.MatchConfig{PathPrefix: "/api/"}, Upstream: "generic"},
	})

	if _, ok := table.Match("gateway.local", "/other/", "GET"); ok {
		t.Error("Match: expected no match, got one")
	}
}

func TestMatch_MethodFiltering(t *testing.T) {
	table := Build([]config.RouteConfig{
		{
			Match:    config.MatchConfig{PathPrefix: "/api/", Methods: []string{"GET", "POST"}},
			Upstream: "generic",
		},
	})

	if _, ok := table.Match("gateway.local", "/api/x", "DELETE"); ok {
		t.Error("Match: DELETE should not match a GET/POST-only route")
	}
	if _, ok := table.Match("gateway.local", "/api/x", "get"); !ok {
		t.Error("Match: method comparison should be case-insensitive")
	}
}

func TestMatch_EmptyMethodsMeansAny(t *testing.T) {
	table := Build([]config.RouteConfig{
		{Match: config.MatchConfig{PathPrefix: "/api/"}, Upstream: "generic"},
	})
	for _, m := range []string{"GET", "POST", "DELETE", "PATCH"} {
		if _, ok := table.Match("gateway.local", "/api/x", m); !ok {
			t.Errorf("Match: method %s should match a route with no methods restriction", m)
		}
	}
}

func TestMatch_HostFiltering(t *testing.T) {
	table := Build([]config.RouteConfig{
		{Match: config.MatchConfig{Host: "a.example.com", PathPrefix: "/"}, Upstream: "a"},
		{Match: config.MatchConfig{Host: "b.example.com", PathPrefix: "/"}, Upstream: "b"},
		{Match: config.MatchConfig{PathPrefix: "/shared/"}, Upstream: "shared"},
	})

	route, ok := table.Match("a.example.com:8080", "/x", "GET")
	if !ok || route.Upstream != "a" {
		t.Errorf("Match(a.example.com) = %v, %v, want upstream %q", route, ok, "a")
	}

	route, ok = table.Match("b.example.com", "/x", "GET")
	if !ok || route.Upstream != "b" {
		t.Errorf("Match(b.example.com) = %v, %v, want upstream %q", route, ok, "b")
	}

	if _, ok := table.Match("a.example.com", "/shared/x", "GET"); !ok {
		t.Error("Match: host-less route should match any host")
	}

	if _, ok := table.Match("c.example.com", "/x", "GET"); ok {
		t.Error("Match: unknown host should not match a host-scoped route")
	}
}

func TestMatch_TieKeepsFirstDeclared(t *testing.T) {
	table := Build([]config.RouteConfig{
		{Match: config.MatchConfig{PathPrefix: "/api/"}, Upstream: "first"},
		{Match: config.MatchConfig{PathPrefix: "/api/"}, Upstream: "second"},
	})

	route, ok := table.Match("gateway.local", "/api/x", "GET")
	if !ok || route.Upstream != "first" {
		t.Errorf("Match = %v, %v, want first-declared upstream %q on a tie", route, ok, "first")
	}
}

func TestStripPath(t *testing.T) {
	tests := []struct {
		stripPrefix string
		path        string
		want        string
	}{
		{"", "/api/floorplan/render", "/api/floorplan/render"},
		{"/api/floorplan", "/api/floorplan/render", "/render"},
		{"/api/floorplan", "/api/floorplan", "/"},
	}
	for _, tt := range tests {
		r := &Route{StripPrefix: tt.stripPrefix}
		if got := r.StripPath(tt.path); got != tt.want {
			t.Errorf("StripPath(%q) with prefix %q = %q, want %q", tt.path, tt.stripPrefix, got, tt.want)
		}
	}
}
