package slogx

import "context"

// traceContextKey is the private key under which request trace information is
// stored in a context.Context. Keeping it in this package lets the production
// slog handler read the trace without the middleware that populates it having
// to import the handler, avoiding an import cycle.
type traceContextKey struct{}

// TraceInfo carries the Cloud Trace identifiers parsed from an inbound request.
// SpanID is optional and, when set, is the 16-character hexadecimal form
// expected by Cloud Logging's logging.googleapis.com/spanId field. Sampled
// reflects the trace-context sampling flag (the ";o=1" option) and is emitted as
// logging.googleapis.com/trace_sampled when a trace is present.
type TraceInfo struct {
	TraceID string
	SpanID  string
	Sampled bool
}

// ContextWithTrace returns a copy of ctx carrying the given trace identifiers.
// Middleware calls this once per request; the production handler reads it back
// via [TraceFromContext]. It is shorthand for [ContextWithTraceInfo] with the
// sampling flag left unset; use ContextWithTraceInfo to carry Sampled.
func ContextWithTrace(ctx context.Context, traceID, spanID string) context.Context {
	return ContextWithTraceInfo(ctx, TraceInfo{TraceID: traceID, SpanID: spanID})
}

// ContextWithTraceInfo returns a copy of ctx carrying the given trace
// information, including the sampling flag. The tracehttp middleware uses this so
// inbound-header parsing and log emission share one representation of a trace.
func ContextWithTraceInfo(ctx context.Context, info TraceInfo) context.Context {
	return context.WithValue(ctx, traceContextKey{}, info)
}

// TraceFromContext returns the trace information stored in ctx, if any.
func TraceFromContext(ctx context.Context) (TraceInfo, bool) {
	info, ok := ctx.Value(traceContextKey{}).(TraceInfo)
	return info, ok
}
