package slogx

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

const (
	colorReset   = "\033[0m"
	colorRed     = "\033[31m"
	colorYellow  = "\033[33m"
	colorCyan    = "\033[36m"
	colorGray    = "\033[37m"
	colorDimGray = "\033[90m"
)

type colorizer struct {
	enabled bool
}

func (c colorizer) code(color string) string {
	if !c.enabled {
		return ""
	}
	return color
}

// groupedAttr is a handler attribute together with the group prefix that was
// open when it was added, so it renders under the right group at write time.
type groupedAttr struct {
	prefix string
	attr   slog.Attr
}

type plainTextHandler struct {
	opts        slog.HandlerOptions
	out         io.Writer
	mu          *sync.Mutex // shared across derived handlers; serializes writes
	colorizer   colorizer
	groupPrefix string
	attrs       []groupedAttr
}

func newPlainTextHandler(out io.Writer, opts *slog.HandlerOptions, useColor bool) *plainTextHandler {
	if opts == nil {
		opts = &slog.HandlerOptions{}
	}

	return &plainTextHandler{
		opts:      *opts,
		out:       out,
		mu:        &sync.Mutex{},
		colorizer: colorizer{enabled: useColor},
	}
}

func (h *plainTextHandler) Enabled(_ context.Context, level slog.Level) bool {
	minLevel := slog.LevelInfo
	if h.opts.Level != nil {
		minLevel = h.opts.Level.Level()
	}
	return level >= minLevel
}

func (h *plainTextHandler) Handle(_ context.Context, record slog.Record) error {
	buf := make([]byte, 0, 1024)
	buf = record.Time.AppendFormat(buf, "2006-01-02 15:04:05.000")
	buf = append(buf, "  "...)

	buf = append(buf, h.colorizer.code(getLevelColor(record.Level))...)
	buf = appendPadded(buf, strings.ToUpper(record.Level.String()), 6)
	buf = append(buf, h.colorizer.code(colorReset)...)
	buf = append(buf, ' ')

	if h.opts.AddSource && record.PC != 0 {
		if source := formatSource(record.PC); source != "" {
			buf = appendPadded(buf, source, 32)
			buf = append(buf, ' ')
		}
	}

	buf = append(buf, record.Message...)
	buf = h.appendAttributes(buf, record)
	buf = append(buf, '\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.out.Write(buf)
	return err
}

func (h *plainTextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}

	newAttrs := make([]groupedAttr, len(h.attrs), len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	for _, attr := range attrs {
		newAttrs = append(newAttrs, groupedAttr{prefix: h.groupPrefix, attr: attr})
	}

	clone := *h
	clone.attrs = newAttrs
	return &clone
}

func (h *plainTextHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}

	clone := *h
	clone.groupPrefix = h.groupPrefix + name + "."
	return &clone
}

// appendAttributes renders the handler's stored attributes followed by the
// record's per-call attributes (matching the ordering of slog's built-in
// handlers), each namespaced by the group that was open when it was added.
func (h *plainTextHandler) appendAttributes(buf []byte, record slog.Record) []byte {
	if len(h.attrs) == 0 && record.NumAttrs() == 0 {
		return buf
	}

	start := len(buf)
	first := true
	buf = append(buf, ' ')
	buf = append(buf, h.colorizer.code(colorDimGray)...)
	buf = append(buf, "{ "...)
	buf = append(buf, h.colorizer.code(colorReset)...)

	for _, ga := range h.attrs {
		buf = h.appendAttr(buf, ga.prefix, ga.attr, &first)
	}
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key != slog.SourceKey {
			buf = h.appendAttr(buf, h.groupPrefix, attr, &first)
		}
		return true
	})

	// Everything resolved to nothing (e.g. only empty groups): drop the braces.
	if first {
		return buf[:start]
	}

	buf = append(buf, h.colorizer.code(colorDimGray)...)
	buf = append(buf, " }"...)
	buf = append(buf, h.colorizer.code(colorReset)...)
	return buf
}

// appendAttr renders a single attribute under prefix. Group values are flattened
// with dotted key prefixes; empty groups and empty-key attributes are dropped,
// following slog's semantics.
func (h *plainTextHandler) appendAttr(buf []byte, prefix string, attr slog.Attr, first *bool) []byte {
	attr.Value = attr.Value.Resolve()

	if attr.Value.Kind() == slog.KindGroup {
		groupAttrs := attr.Value.Group()
		if len(groupAttrs) == 0 {
			return buf
		}
		if attr.Key != "" {
			prefix += attr.Key + "."
		}
		for _, ga := range groupAttrs {
			buf = h.appendAttr(buf, prefix, ga, first)
		}
		return buf
	}

	if attr.Key == "" {
		return buf
	}

	if !*first {
		buf = append(buf, h.colorizer.code(colorDimGray)...)
		buf = append(buf, ", "...)
		buf = append(buf, h.colorizer.code(colorReset)...)
	}
	*first = false

	buf = append(buf, h.colorizer.code(colorDimGray)...)
	buf = append(buf, '"')
	buf = append(buf, h.colorizer.code(colorReset)...)
	buf = append(buf, h.colorizer.code(colorCyan)...)
	buf = append(buf, prefix...)
	buf = append(buf, attr.Key...)
	buf = append(buf, h.colorizer.code(colorReset)...)

	value, quote := formatAttrValue(attr.Value)
	buf = append(buf, h.colorizer.code(colorDimGray)...)
	buf = append(buf, "\": "...)
	buf = append(buf, h.colorizer.code(colorReset)...)
	if quote {
		// strconv.AppendQuote wraps the value in quotes and escapes embedded
		// quotes, newlines and control characters, keeping each entry on one line.
		buf = strconv.AppendQuote(buf, value)
	} else {
		buf = append(buf, value...)
	}
	buf = append(buf, h.colorizer.code(colorReset)...)

	return buf
}

func formatAttrValue(value slog.Value) (string, bool) {
	if value.Kind() == slog.KindAny {
		if err, ok := value.Any().(error); ok && err != nil {
			return err.Error(), true
		}

		jsonValue, err := json.Marshal(value.Any())
		if err == nil {
			return string(jsonValue), false
		}
	}

	return value.String(), true
}

// appendPadded appends s left-aligned in a field of the given width, padding
// with spaces. Longer strings are not truncated (matching fmt's "%-Ns").
func appendPadded(buf []byte, s string, width int) []byte {
	buf = append(buf, s...)
	for i := len(s); i < width; i++ {
		buf = append(buf, ' ')
	}
	return buf
}

func formatSource(pc uintptr) string {
	frame, _ := runtime.CallersFrames([]uintptr{pc}).Next()
	if frame.File == "" {
		return ""
	}

	var b strings.Builder
	b.WriteByte('[')
	b.WriteString(shortDir(frame.File))
	b.WriteByte('/')
	b.WriteString(filepath.Base(frame.File))
	b.WriteByte(':')
	b.WriteString(strconv.Itoa(frame.Line))
	b.WriteByte(']')
	return b.String()
}

func shortDir(file string) string {
	dir := filepath.Dir(file)
	parts := strings.Split(dir, string(filepath.Separator))
	if len(parts) >= 2 {
		dir = filepath.Join(parts[len(parts)-2], parts[len(parts)-1])
	} else if len(parts) == 1 {
		dir = parts[0]
	}

	if len(dir) <= 16 {
		return dir
	}
	return dir[len(dir)-16:]
}

func getLevelColor(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return colorRed
	case level >= slog.LevelWarn:
		return colorYellow
	case level >= slog.LevelInfo:
		return colorCyan
	default:
		return colorGray
	}
}

// isTerminal reports whether f is a character device (a terminal), used to
// decide whether ANSI color is appropriate. It stays dependency-free by
// inspecting the file mode rather than pulling in golang.org/x/term.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
