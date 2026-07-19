// Package slogx configures the standard library's log/slog for the two output
// modes a typical service needs: structured Google Cloud Logging JSON when
// deployed, and pretty colorized text when developing locally.
//
// It is intentionally dependency-free (standard library only) so it can be
// dropped into any project without pulling in a logging framework.
//
// # Modes, not environments
//
// slogx owns exactly two output modes, [Plain] and [Structured] — the output
// format, which is the only thing the library legitimately owns. It does not
// model your deployment environments. A project with staging/qa/prod stages
// maps its own environment vocabulary onto these two modes, either explicitly:
//
//	slogx.Setup(slogx.Structured, projectID) // staging, qa, prod
//	slogx.Setup(slogx.Plain, "")              // local
//
// or via the [ModeFromEnv] convenience mapper, which treats only local-ish
// values as Plain and everything else as Structured.
package slogx

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// LevelCritical is a severity above slog.LevelError. When emitted in
// [Structured] mode it is rendered as Cloud Logging's "CRITICAL" severity.
const LevelCritical = slog.Level(12)

// Mode selects the logger's output format. The zero value is [Structured], so
// an unset Mode defaults to machine-readable JSON — the safe choice for
// anything deployed.
type Mode int

const (
	// Structured emits Google Cloud Logging JSON to stdout. Zero value.
	Structured Mode = iota
	// Plain emits pretty, colorized, human-readable text to stdout.
	Plain
)

// String returns the mode's name, for logging and debugging.
func (m Mode) String() string {
	if m == Plain {
		return "plain"
	}
	return "structured"
}

// ModeFromEnv maps a free-form environment name onto an output mode. Local-ish
// values ("", "dev", "development", "local") map to [Plain]; every other value
// maps to [Structured]. Defaulting the unknown case to Structured means
// anything deployed emits machine-readable logs even under an environment name
// slogx does not recognize. It is optional sugar: projects can also select the
// Mode directly.
func ModeFromEnv(name string) Mode {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "dev", "development", "local":
		return Plain
	default:
		return Structured
	}
}

// Setup installs the process-wide slog default handler for the given mode. In
// [Structured] mode it emits Cloud Logging JSON; projectID (the GCP project,
// e.g. "olens-lv") is used to build the logging.googleapis.com/trace field so
// in-request logs correlate with their Cloud Run request trace. An empty
// projectID degrades gracefully: logs are still emitted, just without the trace
// field. In [Plain] mode the projectID argument is unused.
func Setup(mode Mode, projectID string) {
	switch mode {
	case Plain:
		slog.SetDefault(slog.New(newPlainTextHandler(os.Stdout, &slog.HandlerOptions{
			AddSource: true,
			Level:     slog.LevelDebug,
		}, true)))
	default:
		jsonHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			AddSource: true,
			Level:     slog.LevelInfo,
			ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
				switch attr.Key {
				case slog.MessageKey:
					attr.Key = "message"
				case slog.SourceKey:
					attr.Key = "logging.googleapis.com/sourceLocation"
				case slog.LevelKey:
					attr.Key = "severity"
					if level, ok := attr.Value.Any().(slog.Level); ok && level == LevelCritical {
						attr.Value = slog.StringValue("CRITICAL")
					}
				}
				return attr
			},
		})
		slog.SetDefault(slog.New(&traceHandler{inner: jsonHandler, projectID: projectID}))
	}

	slog.Info("Logger setup complete", "mode", mode.String())
}

// traceHandler wraps a JSON slog.Handler and, for records whose context carries
// request trace information, injects the Cloud Logging special fields
// logging.googleapis.com/trace and logging.googleapis.com/spanId so the entry
// is grouped under its Cloud Run request trace in the Logs Explorer.
type traceHandler struct {
	inner     slog.Handler
	projectID string
}

func (h *traceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *traceHandler) Handle(ctx context.Context, record slog.Record) error {
	if info, ok := TraceFromContext(ctx); ok && h.projectID != "" && info.TraceID != "" {
		record = record.Clone()
		record.AddAttrs(slog.String("logging.googleapis.com/trace", "projects/"+h.projectID+"/traces/"+info.TraceID))
		if info.SpanID != "" {
			record.AddAttrs(slog.String("logging.googleapis.com/spanId", info.SpanID))
		}
	}
	return h.inner.Handle(ctx, record)
}

func (h *traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceHandler{inner: h.inner.WithAttrs(attrs), projectID: h.projectID}
}

func (h *traceHandler) WithGroup(name string) slog.Handler {
	return &traceHandler{inner: h.inner.WithGroup(name), projectID: h.projectID}
}
