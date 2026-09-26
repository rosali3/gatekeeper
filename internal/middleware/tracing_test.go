package middleware

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// TestMain installs a real (always-sampling) TracerProvider: the default
// global one is a no-op that hands out invalid span contexts, which
// would make every test below meaningless.
func TestMain(m *testing.M) {
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	otel.SetTextMapPropagator(propagation.TraceContext{})
	os.Exit(m.Run())
}

func TestTracing_AttachesASpanToTheContext(t *testing.T) {
	var sawSpanContext trace.SpanContext
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawSpanContext = trace.SpanContextFromContext(r.Context())
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	Tracing("test-tracer")(next).ServeHTTP(httptest.NewRecorder(), req)

	if !sawSpanContext.IsValid() {
		t.Error("expected a valid span context to be attached to the request")
	}
}

func TestTracing_ExtractsIncomingTraceparent(t *testing.T) {
	// A syntactically valid W3C traceparent: version-traceid-spanid-flags.
	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

	var sawTraceID string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawTraceID = trace.SpanContextFromContext(r.Context()).TraceID().String()
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("traceparent", traceparent)
	Tracing("test-tracer")(next).ServeHTTP(httptest.NewRecorder(), req)

	if sawTraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace id = %q, want the one from the incoming traceparent header", sawTraceID)
	}
}

func TestTracing_CallsNextHandler(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })

	Tracing("test-tracer")(next).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

	if !called {
		t.Error("Tracing did not call the next handler")
	}
}
