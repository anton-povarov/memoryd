// Package logging centralizes construction of memoryd's structured logger.
package logging

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/anton-povarov/memoryd/server/internal/config"
)

type contextKey struct{}

// WithLogger associates a structured logger with work derived from ctx.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	if logger == nil {
		logger = slog.Default()
	}
	return context.WithValue(ctx, contextKey{}, logger)
}

// FromContext returns the request-scoped logger when one is present.
func FromContext(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(contextKey{}).(*slog.Logger); ok && logger != nil {
		return logger
	}
	return slog.Default()
}

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
	return handlerForOutput(cfg, writer, developmentEnabled(os.Getenv("MEMORYD_DEV")))
}

func handlerForOutput(cfg config.LoggingConfig, writer io.Writer, development bool) (slog.Handler, error) {
	if writer == nil {
		return nil, fmt.Errorf("logging writer must not be nil")
	}
	level, err := ParseLevel(cfg.Level)
	if err != nil {
		return nil, err
	}
	if development {
		return newDevHandler(writer, level), nil
	}
	return slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: level}), nil
}

func newDevHandler(writer io.Writer, level slog.Level) slog.Handler {
	return &devHandler{writer: writer, level: level, mutex: &sync.Mutex{}}
}

func developmentEnabled(value string) bool {
	enabled, err := strconv.ParseBool(strings.TrimSpace(value))
	return err == nil && enabled
}

const timestampLayout = "2006-01-02 15:04:05 .000000"

const (
	ansiReset  = "\x1b[0m"
	ansiBlue   = "\x1b[34m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
)

// devHandler keeps development logs readable while retaining structured
// attributes as an indented JSON object.
type devHandler struct {
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

func (h *devHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *devHandler) Handle(_ context.Context, record slog.Record) error {
	var line strings.Builder
	line.WriteString("")
	line.WriteString(record.Time.Format(timestampLayout))
	line.WriteString(" ")
	writeLevel(&line, record.Level)
	line.WriteString("  ")
	writeMessage(&line, record.Message)

	h.writeJSONAttrs(&line, record)
	line.WriteByte('\n')

	h.mutex.Lock()
	defer h.mutex.Unlock()
	_, err := io.WriteString(h.writer, line.String())
	return err
}

func (h *devHandler) writeJSONAttrs(line *strings.Builder, record slog.Record) {
	if len(h.attrs) == 0 && record.NumAttrs() == 0 {
		return
	}

	object := make(map[string]any)
	for _, attr := range h.attrs {
		addJSONAttr(object, attr.groups, attr.attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		addJSONAttr(object, h.groups, attr)
		return true
	})
	if len(object) == 0 {
		return
	}

	encoded, err := json.MarshalIndent(object, "", "    ")
	if err != nil {
		encoded = []byte(strconv.Quote(fmt.Sprintf("could not encode log attributes: %v", err)))
	}
	line.WriteByte(' ')
	line.Write(encoded)
}

func addJSONAttr(object map[string]any, groups []string, attr slog.Attr) {
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
			addJSONAttr(object, nestedGroups, nested)
		}
		return
	}

	target := object
	for _, group := range groups {
		nested, ok := target[group].(map[string]any)
		if !ok {
			nested = make(map[string]any)
			target[group] = nested
		}
		target = nested
	}
	target[attr.Key] = jsonValue(attr.Value)
}

func jsonValue(value slog.Value) any {
	if value.Kind() == slog.KindDuration {
		return value.Duration().String()
	}
	if value.Kind() == slog.KindAny {
		if err, ok := value.Any().(error); ok {
			return err.Error()
		}
	}
	return value.Any()
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

func (h *devHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
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

func (h *devHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := *h
	clone.groups = append(append([]string(nil), h.groups...), name)
	return &clone
}

func writeMessage(line *strings.Builder, message string) {
	if strings.ContainsAny(message, "\r\n") {
		line.WriteString(strconv.Quote(message))
		return
	}
	line.WriteString(message)
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
