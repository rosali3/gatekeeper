package main

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

// setupTracing installs the global TracerProvider and W3C Trace Context
// propagator that internal/middleware.Tracing and the proxy's traceparent
// injection (internal/gateway/proxy.go) both rely on being set - otel's
// defaults are no-ops otherwise.
//
// No exporter is registered yet: spans are created and propagated (the
// TZ's actual requirement - "контекст пробрасывается в апстрим через
// traceparent") but not shipped anywhere. The docker-compose stage adds a
// real OTLP exporter pointed at Jaeger via sdktrace.WithBatcher on this
// same provider, without touching this function's callers.
func setupTracing() (*sdktrace.TracerProvider, error) {
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName("gatekeeper"),
	))
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return tp, nil
}

func shutdownTracing(ctx context.Context, tp *sdktrace.TracerProvider) {
	_ = tp.Shutdown(ctx)
}
