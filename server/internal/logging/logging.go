// Package logging centralizes construction of memoryd's structured logger.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anton-povarov/memoryd/server/internal/config"
	"golang.org/x/term"
)

// New builds a logger writing to stderr.  The format and level are validated
// here as well as by config.Config so callers that construct a config directly
// get the same safe behavior.
func New(cfg config.LoggingConfig) (*slog.Logger, error) {
	return NewWithWriter(cfg, os.Stderr)
}

// NewFromConfig is a convenience for callers that keep the complete process
// configuration together.
func NewFromConfig(cfg config.Config) (*slog.Logger, error) {
	return New(cfg.Logging)
}

// NewWithWriter is useful for tests and for applications embedding memoryd.
func NewWithWriter(cfg config.LoggingConfig, writer io.Writer) (*slog.Logger, error) {
	h, err := Handler(cfg, writer)
	if err != nil {
		return nil, err
	}
	return slog.New(h), nil
}

// Must builds a logger and panics if cfg is invalid. It is intended for
// process bootstrap after configuration loading, where an invalid setting is
// unrecoverable.
func Must(cfg config.LoggingConfig) *slog.Logger {
	logger, err := New(cfg)
	if err != nil {
		panic(err)
	}
	return logger
}

// Handler constructs the slog handler corresponding to cfg.
func Handler(cfg config.LoggingConfig, writer io.Writer) (slog.Handler, error) {
	return handlerForOutput(cfg, writer, developmentEnabled(os.Getenv("MEMORYD_DEV")), writesToTerminal(writer))
}

func handlerForOutput(cfg config.LoggingConfig, writer io.Writer, development, terminal bool) (slog.Handler, error) {
	if writer == nil {
		return nil, fmt.Errorf("logging writer must not be nil")
	}
	level, err := ParseLevel(cfg.Level)
	if err != nil {
		return nil, err
	}
	format := strings.ToLower(strings.TrimSpace(cfg.Format))
	if format != "text" && format != "json" {
		return nil, fmt.Errorf("logging.format must be text or json (got %q)", cfg.Format)
	}
	if development && terminal {
		return newDevTextHandler(writer, level), nil
	}
	switch format {
	case "text":
		return slog.NewTextHandler(writer, &slog.HandlerOptions{Level: level}), nil
	case "json":
		return slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: level}), nil
	}
	panic("validated logging format is unreachable")
}

func newDevTextHandler(writer io.Writer, level slog.Level) slog.Handler {
	return &devtextHandler{writer: writer, level: level, mutex: &sync.Mutex{}}
}

func developmentEnabled(value string) bool {
	enabled, err := strconv.ParseBool(strings.TrimSpace(value))
	return err == nil && enabled
}

type fileDescriptor interface {
	Fd() uintptr
}

func writesToTerminal(writer io.Writer) bool {
	file, ok := writer.(fileDescriptor)
	return ok && term.IsTerminal(int(file.Fd()))
}

const timestampLayout = "2006-01-02 15:04:05 .000000"

const (
	ansiBold      = "\x1b[1m"
	ansiUnderline = "\x1b[4m"
	ansiReset     = "\x1b[0m"
	ansiBlue      = "\x1b[34m"
	ansiGreen     = "\x1b[32m"
	ansiYellow    = "\x1b[33m"
	ansiRed       = "\x1b[31m"
)

// devtextHandler keeps console logs compact while retaining slog's structured
// attributes. It is selected only for an interactive development terminal.
type devtextHandler struct {
	writer io.Writer
	level  slog.Level
	mutex  *sync.Mutex
	attrs  []boundAttr
	groups []string
}

type boundAttr struct {
	groups []string
	attr   slog.Attr
}

func (h *devtextHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *devtextHandler) Handle(_ context.Context, record slog.Record) error {
	var line strings.Builder
	line.WriteString("")
	line.WriteString(record.Time.Format(timestampLayout))
	line.WriteString(" ")
	writeLevel(&line, record.Level)
	line.WriteString("  ")
	writeMessage(&line, record.Message)

	if len(h.attrs) > 0 || record.NumAttrs() > 0 {
		line.WriteString(" ") // an extra space before attributes, helps readability
	}

	for _, attr := range h.attrs {
		h.writeAttr(&line, attr.groups, attr.attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		h.writeAttr(&line, h.groups, attr)
		return true
	})
	line.WriteByte('\n')

	h.mutex.Lock()
	defer h.mutex.Unlock()
	_, err := io.WriteString(h.writer, line.String())
	return err
}

func writeLevel(line *strings.Builder, level slog.Level) {
	color, name := levelColorAndName(level)
	if color != "" {
		line.WriteString(color)
	}
	line.WriteString(name)
	if color != "" {
		line.WriteString(ansiReset)
	}
}

// levelColor follows zerolog's console palette: trace blue, debug uncolored,
// info green, warn yellow, and errors red.
func levelColorAndName(level slog.Level) (string, string) {
	switch {
	case level < slog.LevelDebug:
		return ansiBlue, "TRC"
	case level < slog.LevelInfo:
		return "", "DBG"
	case level < slog.LevelWarn:
		return ansiGreen, "INF"
	case level < slog.LevelError:
		return ansiYellow, "WRN"
	default:
		return ansiRed, "ERR"
	}
}

func (h *devtextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append([]boundAttr(nil), h.attrs...)
	for _, attr := range attrs {
		clone.attrs = append(clone.attrs, boundAttr{
			groups: append([]string(nil), h.groups...),
			attr:   attr,
		})
	}
	return &clone
}

func (h *devtextHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := *h
	clone.groups = append(append([]string(nil), h.groups...), name)
	return &clone
}

func (h *devtextHandler) writeAttr(line *strings.Builder, groups []string, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return
	}
	if attr.Value.Kind() == slog.KindGroup {
		nestedGroups := groups
		if attr.Key != "" {
			nestedGroups = append(append([]string(nil), groups...), attr.Key)
		}
		for _, nested := range attr.Value.Group() {
			h.writeAttr(line, nestedGroups, nested)
		}
		return
	}

	line.WriteByte(' ')
	if len(groups) > 0 {
		line.WriteString(strings.Join(groups, "."))
		line.WriteByte('.')
	}
	line.WriteString(attr.Key)
	line.WriteByte('=')
	writeValue(line, attr.Value)
}

func writeMessage(line *strings.Builder, message string) {
	if strings.ContainsAny(message, "\r\n") {
		line.WriteString(strconv.Quote(message))
		return
	}
	line.WriteString(message)
}

func writeValue(line *strings.Builder, value slog.Value) {
	switch value.Kind() {
	case slog.KindString:
		writeStringValue(line, value.String())
	case slog.KindBool:
		line.WriteString(strconv.FormatBool(value.Bool()))
	case slog.KindInt64:
		line.WriteString(strconv.FormatInt(value.Int64(), 10))
	case slog.KindUint64:
		line.WriteString(strconv.FormatUint(value.Uint64(), 10))
	case slog.KindFloat64:
		line.WriteString(strconv.FormatFloat(value.Float64(), 'g', -1, 64))
	case slog.KindDuration:
		line.WriteString(value.Duration().String())
	case slog.KindTime:
		writeStringValue(line, value.Time().Format(time.RFC3339Nano))
	case slog.KindAny:
		writeStringValue(line, fmt.Sprint(value.Any()))
	default:
		writeStringValue(line, value.String())
	}
}

func writeStringValue(line *strings.Builder, value string) {
	if value == "" || strings.ContainsAny(value, " \t\r\n=\"") {
		line.WriteString(strconv.Quote(value))
		return
	}
	line.WriteString(value)
}

func ParseLevel(value string) (slog.Level, error) {
	level, err := config.ParseLoggingLevel(value)
	if err != nil {
		return 0, err
	}
	switch level {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	panic("validated logging level is unreachable")
}
