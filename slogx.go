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
	"io"
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
		}, isTerminal(os.Stdout))))
	default:
		slog.SetDefault(slog.New(&traceHandler{root: newStructuredHandler(os.Stdout), projectID: projectID}))
	}

	slog.Info("Logger setup complete", "mode", mode.String())
}

// newStructuredHandler builds the Cloud Logging JSON handler: it renames slog's
// standard keys to the fields Cloud Logging recognizes and maps each level onto
// a valid LogSeverity value.
func newStructuredHandler(w io.Writer) slog.Handler {
	return slog.NewJSONHandler(w, &slog.HandlerOptions{
		AddSource:   true,
		Level:       slog.LevelInfo,
		ReplaceAttr: replaceForCloudLogging,
	})
}

func replaceForCloudLogging(_ []string, attr slog.Attr) slog.Attr {
	switch attr.Key {
	case slog.MessageKey:
		attr.Key = "message"
	case slog.SourceKey:
		attr.Key = "logging.googleapis.com/sourceLocation"
	case slog.LevelKey:
		attr.Key = "severity"
		if level, ok := attr.Value.Any().(slog.Level); ok {
			attr.Value = slog.StringValue(severityFor(level))
		}
	}
	return attr
}

// severityFor maps a slog.Level onto Cloud Logging's LogSeverity enum names.
// slog's own level strings do not all match: notably slog.LevelWarn stringifies
// to "WARN", but Cloud Logging expects "WARNING" and treats any unrecognized
// value as DEFAULT severity. Thresholds (rather than equality) also give
// sensible names to custom in-between levels.
func severityFor(level slog.Level) string {
	switch {
	case level >= LevelCritical:
		return "CRITICAL"
	case level >= slog.LevelError:
		return "ERROR"
	case level >= slog.LevelWarn:
		return "WARNING"
	case level >= slog.LevelInfo:
		return "INFO"
	default:
		return "DEBUG"
	}
}

// traceHandler wraps a JSON slog.Handler and, for records whose context carries
// request trace information, injects the Cloud Logging special fields
// logging.googleapis.com/trace and logging.googleapis.com/spanId so the entry
// is grouped under its Cloud Run request trace in the Logs Explorer.
//
// Those special fields must sit at the JSON top level. Cloud Logging does not
// read them when they are nested under a group, so the handler cannot simply add
// them to the record and delegate to a grouped inner handler. Instead it records
// the caller's WithGroup/WithAttrs operations and, in Handle, injects the trace
// fields onto the ungrouped root first and replays the caller's operations on
// top — keeping the trace fields top-level while user attributes still nest
// under their groups.
type traceHandler struct {
	root      slog.Handler                      // base handler, never grouped
	projectID string                            // GCP project for the trace field
	withOps   []func(slog.Handler) slog.Handler // caller WithGroup/WithAttrs, in order
}

func (h *traceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.root.Enabled(ctx, level)
}

func (h *traceHandler) Handle(ctx context.Context, record slog.Record) error {
	handler := h.root

	// Inject the trace fields onto the ungrouped root before replaying the
	// caller's operations, so they land at the top level rather than inside an
	// open group.
	if info, ok := TraceFromContext(ctx); ok && h.projectID != "" && info.TraceID != "" {
		fields := []slog.Attr{
			slog.String("logging.googleapis.com/trace", "projects/"+h.projectID+"/traces/"+info.TraceID),
		}
		if info.SpanID != "" {
			fields = append(fields, slog.String("logging.googleapis.com/spanId", info.SpanID))
		}
		handler = handler.WithAttrs(fields)
	}

	for _, op := range h.withOps {
		handler = op(handler)
	}
	return handler.Handle(ctx, record)
}

func (h *traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	return h.appended(func(inner slog.Handler) slog.Handler { return inner.WithAttrs(attrs) })
}

func (h *traceHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return h.appended(func(inner slog.Handler) slog.Handler { return inner.WithGroup(name) })
}

// appended returns a copy of h with op recorded after the existing operations.
// The slice is copied rather than appended in place so sibling handlers derived
// from the same parent do not share (and clobber) backing storage.
func (h *traceHandler) appended(op func(slog.Handler) slog.Handler) *traceHandler {
	ops := make([]func(slog.Handler) slog.Handler, len(h.withOps)+1)
	copy(ops, h.withOps)
	ops[len(h.withOps)] = op
	return &traceHandler{root: h.root, projectID: h.projectID, withOps: ops}
}
