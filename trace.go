package slogx

import "context"

// traceContextKey is the private key under which request trace information is
// stored in a context.Context. Keeping it in this package lets the production
// slog handler read the trace without the middleware that populates it having
// to import the handler, avoiding an import cycle.
type traceContextKey struct{}

// TraceInfo carries the Cloud Trace identifiers parsed from an inbound request.
// SpanID is optional and, when set, is the 16-character hexadecimal form
// expected by Cloud Logging's logging.googleapis.com/spanId field.
type TraceInfo struct {
	TraceID string
	SpanID  string
}

// ContextWithTrace returns a copy of ctx carrying the given trace identifiers.
// Middleware calls this once per request; the production handler reads it back
// via [TraceFromContext].
func ContextWithTrace(ctx context.Context, traceID, spanID string) context.Context {
	return context.WithValue(ctx, traceContextKey{}, TraceInfo{TraceID: traceID, SpanID: spanID})
}

// TraceFromContext returns the trace information stored in ctx, if any.
func TraceFromContext(ctx context.Context) (TraceInfo, bool) {
	info, ok := ctx.Value(traceContextKey{}).(TraceInfo)
	return info, ok
}
