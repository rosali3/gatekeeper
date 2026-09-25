// Package middleware implements gatekeeper's cross-cutting HTTP concerns as
// plain http.Handler wrappers, chained explicitly in a fixed order (see
// Chain) rather than through a middleware framework.
package middleware

import "net/http"

// Middleware wraps a handler with additional behavior.
type Middleware func(http.Handler) http.Handler

// Chain applies mws to final in order, so mws[0] is outermost (runs first
// on the way in, last on the way out). Chain(final, A, B) behaves as
// A(B(final)).
func Chain(final http.Handler, mws ...Middleware) http.Handler {
	h := final
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}
