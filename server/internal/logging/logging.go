package logging

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	if logger, ok := ctx.Value(contextKey{}).(*slog.Logger); ok &&
		logger != nil {
		return logger
	}
	return slog.Default()
}

// New creates new root logger with empty name, level and writing to writer
func NewRoot(level slog.Level, writer io.Writer) *slog.Logger {
	if developmentEnabled(os.Getenv("MEMORYD_DEV")) {
		return slog.New(newHandlerNode("", nil, newDevHandler(writer, level)))
	} else {
		return slog.New(newHandlerNode("", nil, slog.NewJSONHandler(writer, &slog.HandlerOptions{
			Level: level, AddSource: false, ReplaceAttr: nil,
		})))
	}
}

// NewSubLogger constructs a logger with name = "<parent.name>/<name>",
// parent's level and writing to parent's output.
func NewChildLogger(parent *slog.Logger, name string) *slog.Logger {
	if parent == nil {
		return nil
	}

	pnode := parent.Handler().(*handlerNode)

	var newName string
	if pnode.name == "" {
		newName = name
	} else {
		newName = fmt.Sprintf("%s/%s", pnode.name, name)
	}

	switch ph := pnode.h.(type) {
	case *devHandler:
		clone := *ph
		return slog.New(newHandlerNode(newName, pnode, &clone))
	case *slog.JSONHandler:
		return slog.New(newHandlerNode(newName, pnode, ph.WithAttrs([]slog.Attr{slog.String("__name", newName)})))
	default:
		panic("NewSubLogger: parent logger must be DEVELOPMENT or JSON")
	}
}

// NewRootHandler constructs the root handler
func NewRootHandler(name string, level slog.Level, writer io.Writer, development bool) slog.Handler {
	if writer == nil {
		return nil
	}

	var h slog.Handler
	if development {
		h = newDevHandler(writer, level)
	} else {
		h = slog.NewJSONHandler(writer, &slog.HandlerOptions{
			AddSource: false, Level: level, ReplaceAttr: nil,
		})
	}
	return newHandlerNode(name, nil, h)
}

type handlerNode struct {
	parent *handlerNode
	name   string
	h      slog.Handler
}

func newHandlerNode(name string, parent *handlerNode, h slog.Handler) *handlerNode {
	return &handlerNode{parent: parent, name: name, h: h}
}

func (h *handlerNode) Enabled(ctx context.Context, level slog.Level) bool {
	return h.h.Enabled(ctx, level)
}

func (h *handlerNode) Handle(ctx context.Context, record slog.Record) error {

	type frameInfo struct {
		pkg  string
		file string
		line int
	}

	switch realH := h.h.(type) {
	case *devHandler:
		var pcs [20]uintptr
		pcs_len := runtime.Callers(4, pcs[:])
		frames := runtime.CallersFrames(pcs[:pcs_len])
		packages := make([]frameInfo, 0)
		for {
			frame, more := frames.Next()

			// frame.Function looks like: "://github.com"
			pkgPath := frame.Function
			if strings.Contains(pkgPath, "memoryd") && !strings.Contains(frame.File, ".gen.") {
				// fmt.Printf("%#v %s:%d\n", pkgPath, frame.File, frame.Line)

				// Find the last slash to ignore path slashes, then look for the first dot after it
				lastSlash := strings.LastIndex(pkgPath, "/")
				if lastSlash != -1 {
					pkgPath = pkgPath[lastSlash+1:]
				}

				// find the dot in pkgName.funcName, must be there as calls are fully qualified
				dotIndex := strings.Index(pkgPath, ".")
				if dotIndex != -1 {
					pkgPath = pkgPath[:dotIndex]
				} else {
					break
				}

				if len(packages) == 0 || packages[len(packages)-1].pkg != pkgPath {
					// reduce filename to just the last component
					lastSlash := strings.LastIndex(frame.File, "/")
					if lastSlash != -1 {
						frame.File = frame.File[lastSlash+1:]
					}
					packages = append(packages, frameInfo{pkg: pkgPath, file: frame.File, line: frame.Line})
				}
			}
			if pkgPath == "main" || pkgPath == "runtime" {
				break
			}

			if !more {
				break
			}
		}
		slices.Reverse(packages)

		// last component gets the file:line
		// first component does not get the prefix " > "
		pkgString := ""
		for i, pkg := range packages {
			if i != 0 && len(packages) > 1 {
				pkgString += " > "
			}

			if i == len(packages)-1 {
				pkgString += fmt.Sprintf("%s/%s:%d", pkg.pkg, pkg.file, pkg.line)
			} else {
				pkgString += fmt.Sprintf("%s", pkg.pkg)
			}
		}

		record.Message = fmt.Sprintf(" %s [%s]", record.Message, pkgString)
		return realH.Handle(ctx, record)
	case *slog.JSONHandler:
		return realH.Handle(ctx, record)
	default:
		panic("handlerNode::Handle: logger must be DEVELOPMENT or JSON")
	}
}

func (h *handlerNode) WithAttrs(attrs []slog.Attr) slog.Handler {
	return newHandlerNode(h.name, h.parent, h.h.WithAttrs(attrs))
}

func (h *handlerNode) WithGroup(name string) slog.Handler {
	return newHandlerNode(h.name, h.parent, h.h.WithGroup(name))
}

func newDevHandler(writer io.Writer, level slog.Level) *devHandler {
	return &devHandler{
		writer: writer,
		level:  level,
		mutex:  &sync.Mutex{},
		attrs:  nil,
		groups: nil,
	}
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

func (h *devHandler) Handle(ctx context.Context, record slog.Record) error {
	var line strings.Builder
	line.WriteString("")
	line.WriteString(record.Time.Format(timestampLayout))
	line.WriteString(" ")
	writeLevel(&line, record.Level)
	line.WriteString(" ")
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
		encoded = []byte(
			strconv.Quote(
				fmt.Sprintf("could not encode log attributes: %v", err),
			),
		)
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
	case level < slog.LevelInfo:
		return ansiBlue, "DBG"
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
