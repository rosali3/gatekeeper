package router

import (
	"testing"

	"gatekeeper/internal/config"
)

func BenchmarkTable_Match(b *testing.B) {
	table := Build([]config.RouteConfig{
		{Match: config.MatchConfig{PathPrefix: "/api/a/"}, Upstream: "a"},
		{Match: config.MatchConfig{PathPrefix: "/api/b/"}, Upstream: "b"},
		{Match: config.MatchConfig{PathPrefix: "/api/floorplan/", Methods: []string{"GET", "POST"}}, Upstream: "floorplan"},
		{Match: config.MatchConfig{PathPrefix: "/api/c/"}, Upstream: "c"},
		{Match: config.MatchConfig{PathPrefix: "/api/d/"}, Upstream: "d"},
	})

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		table.Match("gateway.local", "/api/floorplan/render", "GET")
	}
}
