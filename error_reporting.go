package slogx

import (
	"context"
	"log/slog"
	"runtime"
	"strconv"
	"strings"
)

// reportedErrorEventType is the Cloud Error Reporting marker: a log entry
// carrying this "@type" is ingested as a ReportedErrorEvent, so it is grouped,
// counted and alertable in Error Reporting rather than living only in the Logs
// Explorer.
const reportedErrorEventType = "type.googleapis.com/google.devtools.clouderrorreporting.v1beta1.ReportedErrorEvent"

// noReportKey is the attribute key of the [NoReport] marker. It is stripped
// from every record before emission, so it never appears in the output.
const noReportKey = "logging.slogx/no_report"

// NoReport marks a single log record so it is emitted without Error Reporting
// enrichment, even at or above the reporting threshold. Attach it to a known,
// expected error to keep it out of Error Reporting without lowering its level:
//
//	slog.Error("expected upstream 429, retrying", slogx.NoReport)
//
// The marker itself is stripped from the output.
var NoReport = slog.Attr{Key: noReportKey, Value: slog.BoolValue(true)}

// errorReportingHandler wraps another handler and, in [Structured] mode, enriches
// records at or above a threshold so they surface in Cloud Error Reporting: it
// adds the ReportedErrorEvent "@type", a "serviceContext" object, and a
// synthesized "stack_trace" when the record carries none.
//
// Those fields must sit at the JSON top level for Error Reporting to read them —
// nesting "@type" under a group hides it. Like [traceHandler], the handler
// therefore keeps its wrapped root ungrouped, records the caller's
// WithGroup/WithAttrs operations, and in Handle injects the enrichment onto the
// ungrouped root before replaying the caller's operations on top. It performs
// only Error Reporting enrichment; trace correlation is left entirely to
// [traceHandler] so the two never double-inject.
type errorReportingHandler struct {
	root      slog.Handler                      // wrapped handler, never grouped
	threshold slog.Level                        // records at or above this are enriched
	service   string                            // serviceContext.service
	version   string                            // serviceContext.version
	withOps   []func(slog.Handler) slog.Handler // caller WithGroup/WithAttrs, in order
}

func (h *errorReportingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.root.Enabled(ctx, level)
}

func (h *errorReportingHandler) Handle(ctx context.Context, record slog.Record) error {
	// The NoReport marker is always stripped, whether or not the record would
	// otherwise be enriched, so it never reaches the output.
	record, suppressed := stripNoReport(record)

	handler := h.root
	// Inject the enrichment onto the ungrouped root before replaying the caller's
	// operations, so the Error Reporting fields land at the top level rather than
	// inside an open group.
	if !suppressed && record.Level >= h.threshold {
		handler = handler.WithAttrs(h.enrich(record))
	}

	for _, op := range h.withOps {
		handler = op(handler)
	}
	return handler.Handle(ctx, record)
}

func (h *errorReportingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	return h.appended(func(inner slog.Handler) slog.Handler { return inner.WithAttrs(attrs) })
}

func (h *errorReportingHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return h.appended(func(inner slog.Handler) slog.Handler { return inner.WithGroup(name) })
}

// appended returns a copy of h with op recorded after the existing operations.
// The slice is copied rather than appended in place so sibling handlers derived
// from the same parent do not share (and clobber) backing storage.
func (h *errorReportingHandler) appended(op func(slog.Handler) slog.Handler) *errorReportingHandler {
	ops := make([]func(slog.Handler) slog.Handler, len(h.withOps)+1)
	copy(ops, h.withOps)
	ops[len(h.withOps)] = op
	return &errorReportingHandler{
		root:      h.root,
		threshold: h.threshold,
		service:   h.service,
		version:   h.version,
		withOps:   ops,
	}
}

// enrich builds the Error Reporting fields for a qualifying record: the
// ReportedErrorEvent "@type", the "serviceContext" object (omitted only when
// neither service nor version is known), and a synthesized "stack_trace" unless
// the record already carries one.
func (h *errorReportingHandler) enrich(record slog.Record) []slog.Attr {
	fields := []slog.Attr{slog.String("@type", reportedErrorEventType)}
	if serviceContext, ok := h.serviceContext(); ok {
		fields = append(fields, serviceContext)
	}
	if !hasAttr(record, "stack_trace") {
		fields = append(fields, slog.String("stack_trace", synthesizeStack(record)))
	}
	return fields
}

// serviceContext resolves the Error Reporting serviceContext object. It degrades
// gracefully: version is dropped when unset, and the whole object is omitted
// (ok=false) when neither service nor version is known.
func (h *errorReportingHandler) serviceContext() (slog.Attr, bool) {
	var pairs []any
	if h.service != "" {
		pairs = append(pairs, slog.String("service", h.service))
	}
	if h.version != "" {
		pairs = append(pairs, slog.String("version", h.version))
	}
	if len(pairs) == 0 {
		return slog.Attr{}, false
	}
	return slog.Group("serviceContext", pairs...), true
}

// stripNoReport returns record with any [NoReport] marker removed, and reports
// whether one was present. When no marker is present the record is returned
// unchanged to avoid the cost of rebuilding it.
func stripNoReport(record slog.Record) (slog.Record, bool) {
	if !hasAttr(record, noReportKey) {
		return record, false
	}

	stripped := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key != noReportKey {
			stripped.AddAttrs(attr)
		}
		return true
	})
	return stripped, true
}

// hasAttr reports whether record carries an attribute with the given key.
func hasAttr(record slog.Record, key string) bool {
	found := false
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == key {
			found = true
			return false
		}
		return true
	})
	return found
}

// synthesizeStack builds a minimal Go-style stack trace for a record that has
// none, so Error Reporting has something to group by. slog preserves only the
// call site (record.PC), not the full stack, so the synthesized trace is a
// single frame — the log call itself — which is enough to form a stable group.
func synthesizeStack(record slog.Record) string {
	var b strings.Builder
	b.WriteString(record.Message)
	b.WriteString("\n\ngoroutine 1 [running]:\n")
	if record.PC != 0 {
		frame, _ := runtime.CallersFrames([]uintptr{record.PC}).Next()
		if frame.Function != "" {
			b.WriteString(frame.Function)
			b.WriteString("(...)\n\t")
			b.WriteString(frame.File)
			b.WriteByte(':')
			b.WriteString(strconv.Itoa(frame.Line))
			b.WriteByte('\n')
		}
	}
	return b.String()
}
