package slogx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
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

type plainTextHandler struct {
	opts      slog.HandlerOptions
	out       io.Writer
	attrs     []slog.Attr
	colorizer colorizer
}

func newPlainTextHandler(out io.Writer, opts *slog.HandlerOptions, useColor bool) *plainTextHandler {
	if opts == nil {
		opts = &slog.HandlerOptions{}
	}

	return &plainTextHandler{
		opts:      *opts,
		out:       out,
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

	levelColor := getLevelColor(record.Level)
	buf = append(buf, h.colorizer.code(levelColor)...)
	buf = append(buf, fmt.Sprintf("%-6s", strings.ToUpper(record.Level.String()))...)
	buf = append(buf, h.colorizer.code(colorReset)...)
	buf = append(buf, " "...)

	if h.opts.AddSource && record.PC != 0 {
		source := h.formatSource(record.PC)
		if source != "" {
			buf = append(buf, fmt.Sprintf("%-32s", source)...)
			buf = append(buf, " "...)
		}
	}

	buf = append(buf, record.Message...)
	buf = h.appendAttributes(buf, record)
	buf = append(buf, '\n')

	_, err := h.out.Write(buf)
	return err
}

func (h *plainTextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	copy(newAttrs[len(h.attrs):], attrs)

	return &plainTextHandler{
		opts:      h.opts,
		out:       h.out,
		attrs:     newAttrs,
		colorizer: h.colorizer,
	}
}

func (h *plainTextHandler) WithGroup(_ string) slog.Handler {
	return h
}

func (h *plainTextHandler) formatSource(pc uintptr) string {
	frame, _ := runtime.CallersFrames([]uintptr{pc}).Next()
	if frame.File == "" {
		return ""
	}

	return fmt.Sprintf("[%s/%s:%d]", shortDir(frame.File), filepath.Base(frame.File), frame.Line)
}

func (h *plainTextHandler) appendAttributes(buf []byte, record slog.Record) []byte {
	if !h.hasAnyAttributes(record) {
		return buf
	}

	first := true
	buf = append(buf, " "...)
	buf = append(buf, h.colorizer.code(colorDimGray)...)
	buf = append(buf, "{ "...)
	buf = append(buf, h.colorizer.code(colorReset)...)

	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key != slog.SourceKey {
			buf = h.appendAttribute(buf, attr, &first)
		}
		return true
	})

	for _, attr := range h.attrs {
		buf = h.appendAttribute(buf, attr, &first)
	}

	buf = append(buf, h.colorizer.code(colorDimGray)...)
	buf = append(buf, " }"...)
	buf = append(buf, h.colorizer.code(colorReset)...)

	return buf
}

func (h *plainTextHandler) hasAnyAttributes(record slog.Record) bool {
	if len(h.attrs) > 0 {
		return true
	}

	hasAttrs := false
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key != slog.SourceKey {
			hasAttrs = true
			return false
		}
		return true
	})
	return hasAttrs
}

func (h *plainTextHandler) appendAttribute(buf []byte, attr slog.Attr, first *bool) []byte {
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
	buf = append(buf, attr.Key...)
	buf = append(buf, h.colorizer.code(colorReset)...)

	value, quote := formatAttrValue(attr.Value)
	buf = append(buf, h.colorizer.code(colorDimGray)...)
	if quote {
		buf = append(buf, "\": \""...)
		buf = append(buf, value...)
		buf = append(buf, '"')
	} else {
		buf = append(buf, "\": "...)
		buf = append(buf, h.colorizer.code(colorReset)...)
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
