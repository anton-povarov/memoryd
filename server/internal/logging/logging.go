package logging

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"

	"github.com/kylelemons/godebug/pretty"
)

type Level = slog.Level

type Logger struct {
	*slog.Logger
	err error
}

// NewRoot creates a root logger at level, writing to writer.
func New(level slog.Level, writer io.Writer) *Logger {
	if developmentEnabled(os.Getenv("MEMORYD_DEV")) {
		return NewFromSlog(slog.New(newDevHandler(writer, level)))
	}
	return NewFromSlog(slog.New(slog.NewJSONHandler(writer, &slog.HandlerOptions{
		Level: level, AddSource: false, ReplaceAttr: nil,
	})))
}

func NewFromSlog(logger *slog.Logger) *Logger {
	return &Logger{Logger: logger, err: nil}
}

func (l *Logger) IsDevelopment() bool {
	if l == nil {
		return false
	}
	_, ok := l.Handler().(*devHandler)
	return ok
}

func (l *Logger) WithError(err error) *Logger {
	return &Logger{Logger: l.Logger.With("error", err), err: err}
}

func (l *Logger) Enabled(ctx context.Context, level slog.Level) bool {
	if l.err != nil {
		return l.Logger.Enabled(ctx, slog.LevelError)
	}
	return l.Logger.Enabled(ctx, level)
}

func (l *Logger) maybeErrorLevel(requestedLevel slog.Level) slog.Level {
	if l.err != nil {
		return slog.LevelError
	}
	return requestedLevel
}

func (l *Logger) Log(ctx context.Context, level slog.Level, msg string, attrs ...any) {
	l.Logger.Log(ctx, level, msg, attrs...)
}

func (l *Logger) LogAttrs(ctx context.Context, level slog.Level, msg string, attrs ...slog.Attr) {
	l.Logger.LogAttrs(ctx, level, msg, attrs...)
}

func (l *Logger) DebugContext(ctx context.Context, msg string, attrs ...any) {
	l.Logger.Log(ctx, l.maybeErrorLevel(slog.LevelDebug), msg, attrs...)
}

func (l *Logger) InfoContext(ctx context.Context, msg string, attrs ...any) {
	l.Logger.Log(ctx, l.maybeErrorLevel(slog.LevelInfo), msg, attrs...)
}

func (l *Logger) WarnContext(ctx context.Context, msg string, attrs ...any) {
	l.Logger.Log(ctx, l.maybeErrorLevel(slog.LevelWarn), msg, attrs...)
}

func (l *Logger) ErrorContext(ctx context.Context, msg string, attrs ...any) {
	l.Logger.Log(ctx, l.maybeErrorLevel(slog.LevelError), msg, attrs...)
}

func (l *Logger) With(attrs ...any) *Logger {
	return &Logger{Logger: l.Logger.With(attrs...), err: l.err}
}

const (
	systemKey       = "__sys"
	sysOperationKey = "__sys_op"
)

// System returns the reserved attribute used to identify a log system.
func System(name string) slog.Attr {
	return slog.String(systemKey, name)
}

// SysOperation returns the reserved attribute used to identify an operation within a system.
func SysOperation(operation string) slog.Attr {
	return slog.String(sysOperationKey, operation)
}

// -------------------------------------------------------------------------------------------------

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
	ansiReset         = "\x1b[0m"
	ansiBlue          = "\x1b[34m"
	ansiGreen         = "\x1b[32m"
	ansiYellow        = "\x1b[33m"
	ansiRed           = "\x1b[31m"
	ansiSqlBoundParam = "\x1b[48;5;240m" // grey background
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
	attr.Value = resolveDebugValue(attr.Value)
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
	return fmt.Sprint(slogValueToAnyValue(value))
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
	callers := callersFramesForLogging(8)
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

func (h *devHandler) flattenMap(object map[string]any) map[string]any {
	flattened := make(map[string]any)
	for k, v := range object {
		if inner, ok := v.(map[string]any); ok {
			for innerK, innerV := range h.flattenMap(inner) {
				flattened[k+"."+innerK] = innerV
			}
			delete(object, k)
		} else {
			flattened[k] = v
		}
	}
	return flattened
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

	// top level error key, generates a red message
	// and changes log level to error (see Logger methods)
	if e, ok := object["error"]; ok {
		switch e := e.(type) {
		case error:
			object["error"] = ansiRed + e.Error() + ansiReset
		case string:
			object["error"] = ansiRed + e + ansiReset
		}
	}

	object = h.flattenMap(object)

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
			valstr = strings.ReplaceAll(valstr, "\t", "  ")
			valstr = strings.ReplaceAll(valstr, "\n", "\n\t\t\t")
			fmt.Fprintf(w, "\t\t%s\t%v\n", k, valstr)
		}

		w.Flush() // nolint:errcheck

		result := strings.TrimRight(buf.String(), "\r\n")
		return result
	}()

	line.WriteByte(' ')
	line.WriteString(encoded)
}

type debugValue interface {
	DebugLogValue() slog.Value
}

func resolveDebugValue(v slog.Value) (rv slog.Value) {
	orig := v
	defer func() {
		if r := recover(); r != nil {
			rv = slog.AnyValue(fmt.Errorf("LogValue panicked\n%s", resolveDebugValueStack(3, 5)))
		}
	}()

	for i := 0; i < 100; i++ {
		if v.Kind() == slog.KindAny || v.Kind() == slog.KindLogValuer {
			if dv, ok := v.Any().(debugValue); ok {
				v = dv.DebugLogValue()
				continue
			}
		}
		return v
	}
	err := fmt.Errorf("resolveValue called too many times on Value of type %T", orig.Any())
	return slog.AnyValue(err)
}

func resolveDebugValueStack(skip, nFrames int) string {
	pcs := make([]uintptr, nFrames+1)
	n := runtime.Callers(skip+1, pcs)
	if n == 0 {
		return "(no stack)"
	}
	frames := runtime.CallersFrames(pcs[:n])
	var b strings.Builder
	i := 0
	for {
		frame, more := frames.Next()
		fmt.Fprintf(&b, "called from %s (%s:%d)\n", frame.Function, frame.File, frame.Line)
		if !more {
			break
		}
		i++
		if i >= nFrames {
			fmt.Fprintf(&b, "(rest of stack elided)\n")
			break
		}
	}
	return b.String()
}

func addJSONAttr(object map[string]any, groups []string, attr slog.Attr) {
	attr.Value = resolveDebugValue(attr.Value)
	if attr.Equal(slog.Attr{}) {
		return
	}

	// fmt.Printf("attr: %v, groups: %v\n", attr, groups)
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

	// fmt.Printf("%v <== %#v [%v]\n", attr.Key, jsonValue(attr.Value), attr.Value.Kind())
	target[attr.Key] = slogValueToAnyValue(attr.Value)
}

func slogValueToAnyValue(value slog.Value) any {
	if value.Kind() == slog.KindDuration {
		return value.Duration().String()
	}
	anyValue := value.Any()

	if value.Kind() == slog.KindAny {
		if err, ok := anyValue.(error); ok {
			return err.Error()
		}

		// pointer to a primitive type
		rv := reflect.ValueOf(anyValue)
		if rv.Kind() == reflect.Pointer {
			switch rv.Type().Elem().Kind() {
			case reflect.Bool, reflect.String,
				reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
				reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
				reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128:
				if rv.IsNil() {
					return nil
				}
				return rv.Elem().Interface()
			}
		}

		pretty.DefaultConfig.IncludeUnexported = true
		pretty.DefaultConfig.PrintStringers = true
		pretty.DefaultConfig.PrintTextMarshalers = true
		// pretty.DefaultConfig.Compact = true // strings become too long, esp for arrays :-/
		return pretty.Sprint(anyValue)
	}
	return anyValue
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
