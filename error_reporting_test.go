package slogx

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"
)

// newErrorReportingChain builds the Structured-mode chain the way Setup does
// (error reporting wrapping trace wrapping the JSON handler) against a buffer,
// so tests exercise the real composition rather than the handler in isolation.
func newErrorReportingChain(buf *bytes.Buffer, threshold slog.Level, service, version string) slog.Handler {
	return &errorReportingHandler{
		root:      &traceHandler{root: newStructuredHandler(buf), projectID: "olens-lv"},
		threshold: threshold,
		service:   service,
		version:   version,
	}
}

// decode parses the single JSON log line written to buf.
func decode(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &entry); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	return entry
}

func recordAt(level slog.Level, msg string, attrs ...slog.Attr) slog.Record {
	r := slog.NewRecord(time.Date(2026, 5, 17, 12, 30, 0, 0, time.UTC), level, msg, 0)
	r.AddAttrs(attrs...)
	return r
}

func TestErrorReportingEnrichesAtThreshold(t *testing.T) {
	var buf bytes.Buffer
	handler := newErrorReportingChain(&buf, slog.LevelError, "olens", "v1.2.3")

	if err := handler.Handle(context.Background(), recordAt(slog.LevelError, "db write failed")); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	entry := decode(t, &buf)
	if entry["@type"] != reportedErrorEventType {
		t.Fatalf("@type = %v, want %q", entry["@type"], reportedErrorEventType)
	}
	svc, ok := entry["serviceContext"].(map[string]any)
	if !ok {
		t.Fatalf("serviceContext missing or not an object: %v", entry)
	}
	if svc["service"] != "olens" || svc["version"] != "v1.2.3" {
		t.Fatalf("serviceContext = %v, want service=olens version=v1.2.3", svc)
	}
	if _, ok := entry["stack_trace"].(string); !ok {
		t.Fatalf("stack_trace missing or not a string: %v", entry)
	}
}

func TestErrorReportingCriticalIsEnrichedAndMapped(t *testing.T) {
	var buf bytes.Buffer
	handler := newErrorReportingChain(&buf, slog.LevelError, "olens", "")

	if err := handler.Handle(context.Background(), recordAt(LevelCritical, "out of memory")); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	entry := decode(t, &buf)
	if entry["severity"] != "CRITICAL" {
		t.Fatalf("severity = %v, want CRITICAL", entry["severity"])
	}
	if entry["@type"] != reportedErrorEventType {
		t.Fatalf("@type = %v, want ReportedErrorEvent", entry["@type"])
	}
}

func TestErrorReportingBelowThresholdPassesThrough(t *testing.T) {
	var buf bytes.Buffer
	handler := newErrorReportingChain(&buf, slog.LevelError, "olens", "v1")

	if err := handler.Handle(context.Background(), recordAt(slog.LevelWarn, "slow query")); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	entry := decode(t, &buf)
	for _, key := range []string{"@type", "serviceContext", "stack_trace"} {
		if _, ok := entry[key]; ok {
			t.Fatalf("below-threshold record was enriched with %q: %v", key, entry)
		}
	}
}

func TestErrorReportingThresholdIsConfigurable(t *testing.T) {
	var buf bytes.Buffer
	// Only report at or above Critical: a plain Error must pass through unenriched.
	handler := newErrorReportingChain(&buf, LevelCritical, "olens", "v1")

	if err := handler.Handle(context.Background(), recordAt(slog.LevelError, "handled error")); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	if _, ok := decode(t, &buf)["@type"]; ok {
		t.Fatalf("error record enriched below a Critical threshold: %s", buf.String())
	}
}

func TestErrorReportingNoReportMarkerSuppressesAndStrips(t *testing.T) {
	var buf bytes.Buffer
	handler := newErrorReportingChain(&buf, slog.LevelError, "olens", "v1")

	record := recordAt(slog.LevelError, "expected upstream 429", NoReport, slog.String("code", "429"))
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	entry := decode(t, &buf)
	if _, ok := entry["@type"]; ok {
		t.Fatalf("NoReport record was still enriched: %v", entry)
	}
	// The marker itself must not leak into the output...
	if _, ok := entry[noReportKey]; ok {
		t.Fatalf("NoReport marker leaked into output: %v", entry)
	}
	// ...but the rest of the record survives.
	if entry["code"] != "429" {
		t.Fatalf("sibling attribute dropped alongside the marker: %v", entry)
	}
}

func TestErrorReportingNoReportStrippedBelowThreshold(t *testing.T) {
	var buf bytes.Buffer
	handler := newErrorReportingChain(&buf, slog.LevelError, "olens", "v1")

	record := recordAt(slog.LevelInfo, "cache miss", NoReport)
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	if _, ok := decode(t, &buf)[noReportKey]; ok {
		t.Fatalf("NoReport marker leaked into a below-threshold record: %s", buf.String())
	}
}

func TestErrorReportingServiceContextDegradesGracefully(t *testing.T) {
	// version unset -> service-only serviceContext.
	var buf bytes.Buffer
	handler := newErrorReportingChain(&buf, slog.LevelError, "olens", "")
	if err := handler.Handle(context.Background(), recordAt(slog.LevelError, "boom")); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	svc, ok := decode(t, &buf)["serviceContext"].(map[string]any)
	if !ok {
		t.Fatalf("serviceContext missing: %s", buf.String())
	}
	if svc["service"] != "olens" {
		t.Fatalf("serviceContext.service = %v, want olens", svc["service"])
	}
	if _, ok := svc["version"]; ok {
		t.Fatalf("serviceContext.version present despite unset version: %v", svc)
	}

	// Neither service nor version -> serviceContext omitted entirely, but the
	// record is still marked as a ReportedErrorEvent.
	buf.Reset()
	empty := newErrorReportingChain(&buf, slog.LevelError, "", "")
	if err := empty.Handle(context.Background(), recordAt(slog.LevelError, "boom")); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	entry := decode(t, &buf)
	if _, ok := entry["serviceContext"]; ok {
		t.Fatalf("serviceContext present with no service/version: %v", entry)
	}
	if entry["@type"] != reportedErrorEventType {
		t.Fatalf("@type missing when serviceContext is empty: %v", entry)
	}
}

func TestErrorReportingKeepsFieldsTopLevelUnderGroup(t *testing.T) {
	var buf bytes.Buffer
	base := newErrorReportingChain(&buf, slog.LevelError, "olens", "v1")

	grouped := base.WithGroup("req").WithAttrs([]slog.Attr{slog.String("path", "/lv")})
	record := recordAt(slog.LevelError, "handler failed", slog.String("status", "500"))
	if err := grouped.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	entry := decode(t, &buf)
	// Error Reporting fields must be top-level, not nested under "req".
	if entry["@type"] != reportedErrorEventType {
		t.Fatalf("@type not at top level: %v", entry)
	}
	req, ok := entry["req"].(map[string]any)
	if !ok {
		t.Fatalf(`"req" group missing: %v`, entry)
	}
	if _, nested := req["@type"]; nested {
		t.Fatalf("@type was nested under the req group: %v", req)
	}
	// User attributes still nest under the group.
	if req["path"] != "/lv" || req["status"] != "500" {
		t.Fatalf("user attrs not nested under req group: %v", req)
	}
}

func TestErrorReportingUsesExistingStackTrace(t *testing.T) {
	var buf bytes.Buffer
	handler := newErrorReportingChain(&buf, slog.LevelError, "olens", "v1")

	record := recordAt(slog.LevelError, "panic recovered", slog.String("stack_trace", "provided-stack"))
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	if got := decode(t, &buf)["stack_trace"]; got != "provided-stack" {
		t.Fatalf("stack_trace = %v, want the caller-provided value", got)
	}
}

func TestPlainModeNeverEnrichesForErrorReporting(t *testing.T) {
	var buf bytes.Buffer
	// The plain handler is the whole chain in Plain mode; the error reporting
	// handler is never installed, so no enrichment can occur.
	handler := newPlainTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}, false)
	if err := handler.Handle(context.Background(), recordAt(slog.LevelError, "boom")); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	output := buf.String()
	for _, marker := range []string{"@type", "serviceContext", "stack_trace", "ReportedErrorEvent"} {
		if bytes.Contains(buf.Bytes(), []byte(marker)) {
			t.Fatalf("plain output contains Error Reporting marker %q: %s", marker, output)
		}
	}
}

func TestWithoutErrorReportingDisablesEnrichment(t *testing.T) {
	var cfg config
	WithoutErrorReporting()(&cfg)
	if !cfg.errorReportingDisabled {
		t.Fatal("WithoutErrorReporting did not disable enrichment")
	}
}

func TestOptionResolution(t *testing.T) {
	cfg := config{reportThreshold: slog.LevelError}
	WithReportThreshold(LevelCritical)(&cfg)
	WithServiceContext("svc", "ver")(&cfg)
	if cfg.reportThreshold != LevelCritical {
		t.Fatalf("threshold = %v, want Critical", cfg.reportThreshold)
	}
	service, version := cfg.resolveServiceContext()
	if service != "svc" || version != "ver" {
		t.Fatalf("resolveServiceContext() = %q/%q, want svc/ver", service, version)
	}
}

func TestResolveServiceContextFallsBackToCloudRunEnv(t *testing.T) {
	t.Setenv("K_SERVICE", "cloudrun-svc")
	t.Setenv("K_REVISION", "cloudrun-rev-001")

	var cfg config // serviceContextSet is false -> falls back to env
	service, version := cfg.resolveServiceContext()
	if service != "cloudrun-svc" || version != "cloudrun-rev-001" {
		t.Fatalf("resolveServiceContext() = %q/%q, want cloudrun env values", service, version)
	}
}
