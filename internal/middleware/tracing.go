package middleware

import (
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// Tracing extracts an incoming traceparent (W3C Trace Context) if
// present, starts a span for the request, and attaches it to the
// context so the proxy's Rewrite (internal/gateway/proxy.go) can inject
// an updated traceparent into the outbound request - that's the actual
// "context propagated to the upstream" the spec asks for.
//
// Uses the global TracerProvider/propagator (set up once in main) rather
// than taking them as parameters, which is the usual OpenTelemetry
// pattern for app-wide instrumentation: every package that wants a
// tracer just asks otel for one by name instead of threading it through
// every constructor.
func Tracing(tracerName string) Middleware {
	tracer := otel.Tracer(tracerName)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
			ctx, span := tracer.Start(ctx, "gateway.request")
			defer span.End()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
