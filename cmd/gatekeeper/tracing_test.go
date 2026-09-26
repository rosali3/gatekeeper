package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

func TestSetupTracing_InstallsGlobalProviderAndPropagator(t *testing.T) {
	tp, err := setupTracing()
	if err != nil {
		t.Fatalf("setupTracing: unexpected error: %v", err)
	}
	defer shutdownTracing(context.Background(), tp)

	if otel.GetTracerProvider() == nil {
		t.Error("global TracerProvider is nil after setupTracing")
	}

	// A no-op propagator (the default before setupTracing runs) never
	// actually extracts anything, so round-tripping a real traceparent
	// confirms ours is installed and active.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	ctx := otel.GetTextMapPropagator().Extract(req.Context(), propagation.HeaderCarrier(req.Header))
	if ctx == req.Context() {
		t.Error("propagator did not extract anything from a valid traceparent header - is it still the no-op default?")
	}
}
