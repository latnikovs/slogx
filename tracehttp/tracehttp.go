// Package tracehttp closes the inbound half of trace correlation for slogx: it
// parses Cloud Run's X-Cloud-Trace-Context request header and stores the trace
// in the request context via slogx, so structured logs emitted downstream carry
// the logging.googleapis.com/trace field and group under their Cloud Run request
// in the Logs Explorer.
//
// The core slogx package owns the output half (the trace context plumbing and
// the emitted fields) and deliberately imports no net/http; this subpackage adds
// the HTTP entry point, so importing slogx for logging alone stays HTTP-free.
package tracehttp

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/latnikovs/slogx"
)

// header is the Cloud Run trace-context request header.
const header = "X-Cloud-Trace-Context"

// Middleware returns an http.Handler middleware that parses the inbound
// X-Cloud-Trace-Context header and stores the trace in the request context via
// [slogx.ContextWithTraceInfo]. Downstream structured logs emitted with that
// context then carry logging.googleapis.com/trace (and spanId, trace_sampled).
//
// A missing or malformed header is not an error: the request passes through
// unchanged, with no trace in context.
func Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if info, ok := ParseCloudTraceHeader(r.Header.Get(header)); ok {
				r = r.WithContext(slogx.ContextWithTraceInfo(r.Context(), info))
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ParseCloudTraceHeader parses a Cloud Run X-Cloud-Trace-Context header value of
// the form "TRACE_ID/SPAN_ID;o=TRACE_TRUE" and reports whether it carried a
// trace. The span ID is converted from the header's decimal form to the
// 16-character hexadecimal form that logging.googleapis.com/spanId expects; the
// ";o=1" option sets [slogx.TraceInfo].Sampled.
//
// It is exported for reuse and testing. A missing trace ID (empty or malformed
// header) yields ok=false.
func ParseCloudTraceHeader(value string) (slogx.TraceInfo, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return slogx.TraceInfo{}, false
	}

	// TRACE_ID is everything before the first "/"; the remainder is
	// "SPAN_ID;o=..." (both optional). A header with no "/" is trace-only.
	traceID, rest, _ := strings.Cut(value, "/")
	traceID = strings.TrimSpace(traceID)
	if traceID == "" {
		return slogx.TraceInfo{}, false
	}

	info := slogx.TraceInfo{TraceID: traceID}

	spanPart, optPart, hasOpts := strings.Cut(rest, ";")
	if span := decimalSpanToHex(strings.TrimSpace(spanPart)); span != "" {
		info.SpanID = span
	}
	if hasOpts {
		info.Sampled = sampledFromOptions(optPart)
	}

	return info, true
}

// decimalSpanToHex converts the header's decimal span ID to the 16-character,
// zero-padded lowercase hexadecimal form Cloud Logging expects. A zero, empty or
// non-numeric span yields "" (no span ID), matching current middleware behaviour.
func decimalSpanToHex(decimal string) string {
	if decimal == "" {
		return ""
	}
	span, err := strconv.ParseUint(decimal, 10, 64)
	if err != nil || span == 0 {
		return ""
	}
	hex := strconv.FormatUint(span, 16)
	if len(hex) < 16 {
		hex = strings.Repeat("0", 16-len(hex)) + hex
	}
	return hex
}

// sampledFromOptions reports whether the header's option segment sets the
// sampling flag, i.e. contains "o=1" (any non-zero value counts as sampled).
func sampledFromOptions(options string) bool {
	for _, opt := range strings.Split(options, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(opt), "=")
		if !ok || key != "o" {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			return n != 0
		}
	}
	return false
}
