package main

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
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
// An OTLP/gRPC exporter (e.g. to Jaeger) is only wired up if
// OTEL_EXPORTER_OTLP_ENDPOINT is set - the standard OTel env var, used
// by the docker-compose demo. Without it, spans are still created and
// propagated (the TZ's actual requirement) but not shipped anywhere,
// which is all local runs/tests need.
func setupTracing() (*sdktrace.TracerProvider, error) {
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName("gatekeeper"),
	))
	if err != nil {
		return nil, err
	}

	opts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	if endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); endpoint != "" {
		exporter, err := otlptracegrpc.New(context.Background(),
			otlptracegrpc.WithEndpointURL(endpoint),
			otlptracegrpc.WithInsecure(),
		)
		if err != nil {
			return nil, err
		}
		opts = append(opts, sdktrace.WithBatcher(exporter))
	}

	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return tp, nil
}

func shutdownTracing(ctx context.Context, tp *sdktrace.TracerProvider) {
	_ = tp.Shutdown(ctx)
}
