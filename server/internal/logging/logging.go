package logging

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

type contextKey struct{}

// ContextWithLogger associates a structured logger with work derived from ctx.
func ContextWithLogger(ctx context.Context, logger *slog.Logger) context.Context {
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

	pnode, ok := parent.Handler().(*handlerNode)
	if !ok {
		panic("NewChildLogger: parent logger must be created by logging.NewRoot")
	}

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
		clone := *ph
		return slog.New(
			newHandlerNode(
				newName,
				pnode,
				// can't do something like
				// ph.WithAttrs([]slog.Attr{slog.String("__name", newName)}),
				// because json handler does not replace the attr,
				// just appends one more with the same name
				&clone,
			),
		)
	default:
		panic("NewSubLogger: parent logger must be DEVELOPMENT or JSON")
	}
}

func NewSibling(sibling *slog.Logger, name string) *slog.Logger {
	if sibling == nil {
		return nil
	}
	snode, ok := sibling.Handler().(*handlerNode)
	if !ok {
		panic("NewSibling: sibling logger must be created by logging.New* functions")
	}

	return NewChildLogger(slog.New(snode.parent), name)
}

func IsDevelopment(logger *slog.Logger) bool {
	_, ok := logger.Handler().(*handlerNode).h.(*devHandler)
	return ok
}

// newRootHandler constructs the root handler
func newRootHandler(
	name string,
	level slog.Level,
	writer io.Writer,
	development bool,
) slog.Handler {
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

	switch realH := h.h.(type) {
	case *devHandler:
		if record.NumAttrs() > 0 {
			callers := callersFramesForLogging(5)

			var pkgSpec strings.Builder

			// format is like
			// <innermost package>/file:line < (2nd innermost package/file:line) < package3 < ...
			for i, pkg := range callers {
				if i > 0 && callers[i-1].pkg == pkg.pkg {
					continue
				}

				if i != 0 && len(callers) > 1 {
					pkgSpec.WriteString(" < ")
				}

				if (i == 0 || i == 1) && pkg.pkg != "runtime" {
					fmt.Fprintf(&pkgSpec, "%s/%s:%d", pkg.pkg, pkg.file, pkg.line)
				} else {
					fmt.Fprint(&pkgSpec, pkg.pkg)
				}
			}

			record.AddAttrs(slog.String("__source", pkgSpec.String()))
		}

		// format: message [/path/to/logger/name]
		msg := " " + record.Message
		if h.name != "" {
			msg += " [" + h.name + "]"
		}
		record.Message = msg

		return realH.Handle(ctx, record)

	case *slog.JSONHandler:
		record.AddAttrs(slog.String("__name", h.name))
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

	encoded := func() string {
		var buf strings.Builder
		encoder := json.NewEncoder(&buf)
		encoder.SetIndent("", "    ")
		encoder.SetEscapeHTML(false)

		err := encoder.Encode(object)
		if err != nil {
			buf.Reset()
			buf.WriteString("{\n    ")
			buf.WriteString("\"error\": ")
			buf.WriteString(strconv.Quote(
				fmt.Sprintf("could not encode log attributes: %v", err),
			))
			buf.WriteString("\n}")
		}

		// encoder adds a trailing newline it seems
		result := strings.TrimRight(buf.String(), "\r\n")
		return result
	}()

	line.WriteByte(' ')
	line.WriteString(encoded)
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

type callerFrameInfo struct {
	pkg  string
	file string
	line int
}

func callersFramesForLogging(skip int) []callerFrameInfo {
	var pcs [30]uintptr
	pcs_len := runtime.Callers(skip, pcs[:])
	frames := runtime.CallersFrames(pcs[:pcs_len])

	callers := make([]callerFrameInfo, 0, pcs_len)

	for {
		frame, more := frames.Next()
		// fmt.Printf("%#v %s:%d\n", frame.Function, frame.File, frame.Line)

		// frame.Function looks like: "/path/to/package.(*something).Name"
		pkgPath := frame.Function

		pkgInMemoryd := strings.Contains(pkgPath, "memoryd")
		pkgIsMain := strings.HasPrefix(pkgPath, "main.")
		pkgIsRuntime := strings.HasPrefix(pkgPath, "runtime.")
		fileIsGenerated := strings.Contains(frame.File, "memoryd") &&
			strings.Contains(frame.File, ".gen.")

		// NOTE(antoxa): before using continue - check `more` variable

		if (pkgInMemoryd || pkgIsRuntime || pkgIsMain) && !fileIsGenerated {
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

			// if len(callers) == 0 || callers[len(callers)-1].pkg != pkgPath {
			{
				// reduce filename to just the last component
				lastSlash := strings.LastIndex(frame.File, "/")
				if lastSlash != -1 {
					frame.File = frame.File[lastSlash+1:]
				}
				callers = append(
					callers,
					callerFrameInfo{pkg: pkgPath, file: frame.File, line: frame.Line},
				)
			}

			// single entry is enough for main and runtime
			// if pkgPath == "main" || pkgPath == "runtime" {
			// 	break
			// }
		}

		if !more {
			break
		}
	}

	return callers
}
