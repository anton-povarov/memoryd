package logging

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
)

type requestIDContextKey struct{}

// ContextWithRequestID associates a request ID with work derived from ctx.
func ContextWithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDContextKey{}, id)
}

// ForOperation derives a logger with the reserved operation attribute.
func ForOperation(base *slog.Logger, operation string) *slog.Logger {
	return base.With(SysOperation(operation))
}

// ForOperationInRequest derives an operation logger and includes the request
// ID as an ordinary attribute when one is present in ctx.
func ForOperationInRequest(
	base *slog.Logger,
	operation string,
	ctx context.Context,
) *slog.Logger {
	logger := ForOperation(base, operation)
	if requestID, ok := ctx.Value(requestIDContextKey{}).(string); ok && requestID != "" {
		logger = logger.With("request_id", requestID)
	}

	return logger
}

// NewRoot creates a root logger at level, writing to writer.
func NewRoot(level slog.Level, writer io.Writer) *slog.Logger {
	if developmentEnabled(os.Getenv("MEMORYD_DEV")) {
		return slog.New(newDevHandler(writer, level))
	}
	return slog.New(slog.NewJSONHandler(writer, &slog.HandlerOptions{
		Level: level, AddSource: false, ReplaceAttr: nil,
	}))
}

const (
	systemKey       = "__system"
	sysOperationKey = "__sys_operation"
)

// System returns the reserved attribute used to identify a log system.
func System(name string) slog.Attr {
	return slog.String(systemKey, name)
}

// SysOperation returns the reserved attribute used to identify an operation within a system.
func SysOperation(operation string) slog.Attr {
	return slog.String(sysOperationKey, operation)
}

func IsDevelopment(logger *slog.Logger) bool {
	if logger == nil {
		return false
	}
	_, ok := logger.Handler().(*devHandler)
	return ok
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
	writeSpecialSuffix(&line, h.specialAttrs(record))

	h.writeAttrs(&line, record)
	line.WriteByte('\n')

	h.mutex.Lock()
	defer h.mutex.Unlock()
	_, err := io.WriteString(h.writer, line.String())
	return err
}

type devSpecialAttrs struct {
	system       string
	sysOperation string
}

func (h *devHandler) specialAttrs(record slog.Record) devSpecialAttrs {
	var special devSpecialAttrs
	for _, attr := range h.attrs {
		visitSpecialAttrs(attr.attr, &special)
	}
	record.Attrs(func(attr slog.Attr) bool {
		visitSpecialAttrs(attr, &special)
		return true
	})
	return special
}

func visitSpecialAttrs(attr slog.Attr, special *devSpecialAttrs) {
	attr.Value = attr.Value.Resolve()
	if attr.Key == systemKey {
		special.system = attrString(attr.Value)
		return
	}
	if attr.Key == sysOperationKey {
		special.sysOperation = attrString(attr.Value)
		return
	}
	if attr.Value.Kind() == slog.KindGroup {
		for _, nested := range attr.Value.Group() {
			visitSpecialAttrs(nested, special)
		}
	}
}

func attrString(value slog.Value) string {
	if value.Kind() == slog.KindString {
		return value.String()
	}
	return fmt.Sprint(jsonValue(value))
}

func writeSpecialSuffix(line *strings.Builder, special devSpecialAttrs) {
	if special.system == "" && special.sysOperation == "" {
		return
	}

	line.WriteString(" [")
	line.WriteString(special.system)
	if special.system != "" && special.sysOperation != "" {
		line.WriteByte('/')
	}
	line.WriteString(special.sysOperation)
	line.WriteByte(']')
}

func sourceForCurrentCall() string {
	callers := callersFramesForLogging(5)
	var pkgSpec strings.Builder

	// format is like: <innermost package>/file:line < package2/file:line < ...
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

	return pkgSpec.String()
}

func (h *devHandler) writeAttrs(line *strings.Builder, record slog.Record) {
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

	// source needs to be top level always (i.e. not within a group, that might be on this logger)
	object["__source"] = sourceForCurrentCall()

	encoded := func() string {
		var buf strings.Builder

		// check that this object can be encoded as JSON
		// this is only required for compatibility with the JSON handler used in production
		encoder := json.NewEncoder(&buf)
		err := encoder.Encode(object)
		if err != nil {
			fmt.Fprintf(&buf, "\n    error %q\n",
				fmt.Sprintf("json encoding error: %v", err))
			return buf.String()
		}

		buf.Reset()
		w := tabwriter.NewWriter(&buf, 1, 4, 3, ' ', 0)
		w.Write([]byte("\n")) // nolint:errcheck

		for _, k := range slices.Sorted(maps.Keys(object)) {
			valstr := fmt.Sprint(object[k])
			// newlines will break tab columns alignment a bit,
			// so simulate that the next line has empty columns before this one
			// effectively aligning into a multiline structure
			// https://stackoverflow.com/questions/45237529/how-to-add-a-newline-into-tabwriter-in-go
			// i.e.
			// <key>      <value to 1st newline>
			// <empty>    <value to 2nd newline>
			// ...
			valstr = strings.ReplaceAll(valstr, "\r", "")
			valstr = strings.ReplaceAll(valstr, "\n", "\n\t\t")
			fmt.Fprintf(w, "\t\t%s\t%v\n", k, valstr)
		}

		w.Flush() // nolint:errcheck

		result := strings.TrimRight(buf.String(), "\r\n")
		return result
	}()

	line.WriteByte(' ')
	// fmt.Fprintf(line, "%#v\n", object)
	// _ = encoded
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
	if attr.Key == systemKey || attr.Key == sysOperationKey {
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
