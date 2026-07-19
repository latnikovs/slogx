package slogx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestTraceHandlerInjectsTraceFieldsFromContext(t *testing.T) {
	var buf bytes.Buffer
	handler := &traceHandler{root: slog.NewJSONHandler(&buf, nil), projectID: "olens-lv"}

	ctx := ContextWithTrace(context.Background(), "105445aa7843bc8bf206b12000100000", "0000000000000001")
	record := slog.NewRecord(time.Date(2026, 5, 17, 12, 30, 0, 0, time.UTC), slog.LevelInfo, "instructor updated", 0)

	if err := handler.Handle(ctx, record); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	output := buf.String()
	for _, want := range []string{
		`"logging.googleapis.com/trace":"projects/olens-lv/traces/105445aa7843bc8bf206b12000100000"`,
		`"logging.googleapis.com/spanId":"0000000000000001"`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output %q does not contain %q", output, want)
		}
	}
}

func TestTraceHandlerOmitsSpanWhenAbsent(t *testing.T) {
	var buf bytes.Buffer
	handler := &traceHandler{root: slog.NewJSONHandler(&buf, nil), projectID: "olens-lv"}

	ctx := ContextWithTrace(context.Background(), "105445aa7843bc8bf206b12000100000", "")
	record := slog.NewRecord(time.Date(2026, 5, 17, 12, 30, 0, 0, time.UTC), slog.LevelInfo, "msg", 0)

	if err := handler.Handle(ctx, record); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, `"logging.googleapis.com/trace":"projects/olens-lv/traces/105445aa7843bc8bf206b12000100000"`) {
		t.Fatalf("output %q missing trace field", output)
	}
	if strings.Contains(output, "logging.googleapis.com/spanId") {
		t.Fatalf("output %q contains spanId, want none", output)
	}
}

func TestTraceHandlerNoTraceInContext(t *testing.T) {
	var buf bytes.Buffer
	handler := &traceHandler{root: slog.NewJSONHandler(&buf, nil), projectID: "olens-lv"}

	record := slog.NewRecord(time.Date(2026, 5, 17, 12, 30, 0, 0, time.UTC), slog.LevelInfo, "msg", 0)
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	if strings.Contains(buf.String(), "logging.googleapis.com/trace") {
		t.Fatalf("output %q contains trace field without a trace context", buf.String())
	}
}

func TestTraceHandlerEmptyProjectIDSkipsTrace(t *testing.T) {
	var buf bytes.Buffer
	handler := &traceHandler{root: slog.NewJSONHandler(&buf, nil), projectID: ""}

	ctx := ContextWithTrace(context.Background(), "105445aa7843bc8bf206b12000100000", "0000000000000001")
	record := slog.NewRecord(time.Date(2026, 5, 17, 12, 30, 0, 0, time.UTC), slog.LevelInfo, "msg", 0)
	if err := handler.Handle(ctx, record); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	if strings.Contains(buf.String(), "logging.googleapis.com/trace") {
		t.Fatalf("output %q contains trace field with empty project id", buf.String())
	}
}

func TestStructuredSeverityMapping(t *testing.T) {
	cases := []struct {
		level slog.Level
		want  string
	}{
		{slog.LevelDebug, "DEBUG"},
		{slog.LevelInfo, "INFO"},
		{slog.LevelWarn, "WARNING"}, // regression: slog stringifies this as "WARN"
		{slog.LevelError, "ERROR"},
		{LevelCritical, "CRITICAL"},
	}

	for _, tc := range cases {
		var buf bytes.Buffer
		// Build the handler at debug level so every case is emitted regardless of
		// the production threshold.
		handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
			Level:       slog.LevelDebug,
			ReplaceAttr: replaceForCloudLogging,
		})
		record := slog.NewRecord(time.Date(2026, 5, 17, 12, 30, 0, 0, time.UTC), tc.level, "msg", 0)
		if err := handler.Handle(context.Background(), record); err != nil {
			t.Fatalf("Handle() error = %v", err)
		}

		output := buf.String()
		if !strings.Contains(output, `"severity":"`+tc.want+`"`) {
			t.Fatalf("level %v: output %q missing severity %q", tc.level, output, tc.want)
		}
		if tc.level == slog.LevelWarn && strings.Contains(output, `"severity":"WARN"`) {
			t.Fatalf("warn level emitted the invalid Cloud Logging severity %q", "WARN")
		}
	}
}

func TestTraceFieldsStayTopLevelUnderGroup(t *testing.T) {
	var buf bytes.Buffer
	base := &traceHandler{root: newStructuredHandler(&buf), projectID: "olens-lv"}

	// Log through a grouped logger, the case that used to nest the trace fields.
	grouped := base.WithGroup("req").WithAttrs([]slog.Attr{slog.String("path", "/lv")})

	ctx := ContextWithTrace(context.Background(), "105445aa7843bc8bf206b12000100000", "0000000000000001")
	record := slog.NewRecord(time.Date(2026, 5, 17, 12, 30, 0, 0, time.UTC), slog.LevelInfo, "handled", 0)
	record.AddAttrs(slog.String("status", "200"))
	if err := grouped.Handle(ctx, record); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}

	// Trace/span must be top-level keys, not nested under the "req" group.
	if entry["logging.googleapis.com/trace"] != "projects/olens-lv/traces/105445aa7843bc8bf206b12000100000" {
		t.Fatalf("trace field not at top level: %v", entry)
	}
	if entry["logging.googleapis.com/spanId"] != "0000000000000001" {
		t.Fatalf("spanId field not at top level: %v", entry)
	}

	// User attributes must still nest under the group.
	req, ok := entry["req"].(map[string]any)
	if !ok {
		t.Fatalf(`"req" group missing or not an object: %v`, entry)
	}
	if req["path"] != "/lv" || req["status"] != "200" {
		t.Fatalf("user attrs not nested under req group: %v", req)
	}
	if _, nested := req["logging.googleapis.com/trace"]; nested {
		t.Fatalf("trace field was nested under the req group: %v", req)
	}
}

func TestModeFromEnv(t *testing.T) {
	plain := []string{"", "  ", "dev", "development", "Local", " DEVELOPMENT "}
	for _, value := range plain {
		if got := ModeFromEnv(value); got != Plain {
			t.Fatalf("ModeFromEnv(%q) = %q, want plain", value, got)
		}
	}

	structured := []string{"production", "staging", "qa", "test", "prod", "anything-else"}
	for _, value := range structured {
		if got := ModeFromEnv(value); got != Structured {
			t.Fatalf("ModeFromEnv(%q) = %q, want structured", value, got)
		}
	}
}

func TestModeStringAndZeroValue(t *testing.T) {
	var zero Mode
	if zero != Structured {
		t.Fatalf("zero-value Mode = %q, want structured", zero)
	}
	if Plain.String() != "plain" || Structured.String() != "structured" {
		t.Fatalf("Mode.String() = %q/%q, want plain/structured", Plain, Structured)
	}
}

func TestPlainTextHandlerEnabledUsesConfiguredLevel(t *testing.T) {
	handler := newPlainTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelWarn}, false)

	if handler.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("info level enabled, want disabled")
	}
	if !handler.Enabled(context.Background(), slog.LevelError) {
		t.Fatal("error level disabled, want enabled")
	}
}

func TestPlainTextHandlerHandleWritesMessageAndAttributes(t *testing.T) {
	var buf bytes.Buffer
	handler := newPlainTextHandler(&buf, &slog.HandlerOptions{AddSource: true}, false)
	handler = handler.WithAttrs([]slog.Attr{slog.String("scope", "admin")}).(*plainTextHandler)
	record := slog.NewRecord(time.Date(2026, 5, 17, 12, 30, 0, 0, time.UTC), slog.LevelInfo, "created user", 0)
	record.AddAttrs(
		slog.String("email", "admin@example.com"),
		slog.Any("error", errors.New("boom")),
		slog.Any("meta", map[string]string{"id": "123"}),
	)

	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	output := buf.String()
	for _, want := range []string{
		"2026-05-17 12:30:00.000",
		"INFO",
		"created user",
		`"email": "admin@example.com"`,
		`"error": "boom"`,
		`"meta": {"id":"123"}`,
		`"scope": "admin"`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output %q does not contain %q", output, want)
		}
	}
}

func TestPlainTextHandlerHandlesRecordsWithoutAttributes(t *testing.T) {
	var buf bytes.Buffer
	handler := newPlainTextHandler(&buf, nil, true)
	record := slog.NewRecord(time.Date(2026, 5, 17, 12, 30, 0, 0, time.UTC), slog.LevelDebug, "debug message", 0)

	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, colorGray+"DEBUG") {
		t.Fatalf("output %q does not contain colored debug level", output)
	}
	if strings.Contains(output, "{ ") {
		t.Fatalf("output %q contains attributes", output)
	}
}

func TestPlainTextHandlerWithGroupReturnsSameHandler(t *testing.T) {
	handler := newPlainTextHandler(&bytes.Buffer{}, nil, false)

	if got := handler.WithGroup("group"); got != handler {
		t.Fatal("WithGroup() returned a different handler")
	}
}

func TestFormatAttrValue(t *testing.T) {
	if got, quote := formatAttrValue(slog.AnyValue(errors.New("boom"))); got != "boom" || !quote {
		t.Fatalf("error value = %q, %v; want boom true", got, quote)
	}
	if got, quote := formatAttrValue(slog.AnyValue(map[string]string{"id": "123"})); got != `{"id":"123"}` || quote {
		t.Fatalf("json value = %q, %v", got, quote)
	}
	if got, quote := formatAttrValue(slog.StringValue("admin")); got != "admin" || !quote {
		t.Fatalf("string value = %q, %v; want admin true", got, quote)
	}
}

func TestShortDir(t *testing.T) {
	if got := shortDir("/Users/name/project/internal/logger/logger.go"); got != "internal/logger" {
		t.Fatalf("shortDir() = %q, want internal/logger", got)
	}
	if got := shortDir("logger.go"); got != "." {
		t.Fatalf("shortDir(relative) = %q, want .", got)
	}
}

func TestGetLevelColor(t *testing.T) {
	tests := []struct {
		level slog.Level
		want  string
	}{
		{level: slog.LevelError, want: colorRed},
		{level: slog.LevelWarn, want: colorYellow},
		{level: slog.LevelInfo, want: colorCyan},
		{level: slog.LevelDebug, want: colorGray},
	}

	for _, tt := range tests {
		if got := getLevelColor(tt.level); got != tt.want {
			t.Fatalf("getLevelColor(%v) = %q, want %q", tt.level, got, tt.want)
		}
	}
}
