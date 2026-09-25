// Package reqctx carries per-request metadata that's discovered deep in the
// middleware chain (which route matched, which upstream/target served the
// request) back out to the access-log middleware that wraps everything.
package reqctx

import "context"

type accessFieldsKey struct{}

// AccessFields is filled in by inner middleware/handlers as a request is
// processed; the access-log middleware reads it after the handler returns.
// It's stored as a pointer in the context so downstream code can mutate the
// same struct instead of having to thread a new context value through.
type AccessFields struct {
	Route    string
	Upstream string
	Target   string
	UserID   string
}

// WithAccessFields attaches a fresh, zero-valued AccessFields to ctx and
// returns both the new context and the struct to mutate.
func WithAccessFields(ctx context.Context) (context.Context, *AccessFields) {
	f := &AccessFields{}
	return context.WithValue(ctx, accessFieldsKey{}, f), f
}

// AccessFieldsFrom returns the AccessFields stored in ctx, or nil if none
// was attached.
func AccessFieldsFrom(ctx context.Context) *AccessFields {
	f, _ := ctx.Value(accessFieldsKey{}).(*AccessFields)
	return f
}
