package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNewWithWriterUsesJSONAndLevel(t *testing.T) {
	t.Setenv("MEMORYD_DEV", "")
	var output bytes.Buffer
	logger := NewRoot(slog.LevelWarn, &output)
	logger.Info("hidden")
	logger.Warn("visible", "count", 2)
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("logger output is not JSON: %v (%q)", err, output.String())
	}
	if record["msg"] != "visible" || record["level"] != "WARN" {
		t.Fatalf("record = %#v", record)
	}
}

func TestNewWithWriterUsesDevelopmentFormat(t *testing.T) {
	t.Setenv("MEMORYD_DEV", "true")
	var output bytes.Buffer
	logger := NewRoot(slog.LevelDebug, &output)
	logger.Debug("hello", "answer", 42)
	if !strings.Contains(output.String(), "\x1b[34mDBG\x1b[0m hello \n") ||
		!hasTabRow(output.String(), "answer", "42") ||
		!hasTabRowKey(output.String(), "__source") {
		t.Fatalf("text output = %q", output.String())
	}
}

func TestDevelopmentHandlerFormat(t *testing.T) {
	var output bytes.Buffer
	handler := newDevHandler(&output, slog.LevelInfo)
	record := slog.NewRecord(
		time.Date(
			2026,
			9,
			14,
			23,
			57,
			33,
			288000000,
			time.FixedZone("Dubai", 4*60*60),
		),
		slog.LevelInfo,
		"HTTP request",
		0,
	)
	record.Add(
		"method",
		"GET",
		"path",
		"/docs",
		"status",
		308,
		"latency",
		3667*time.Nanosecond,
	)
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	want := "2026-09-14 23:57:33 .288000 \x1b[32mINF\x1b[0m HTTP request \n" +
		"      latency    3.667µs\n      method     GET\n" +
		"      path       /docs\n      status     308\n"
	got := withoutSourceLine(output.String())
	if got != want {
		t.Fatalf("text output = %q, want %q", got, want)
	}
}

func TestDevelopmentHandlerPreservesAttrsAndGroups(t *testing.T) {
	var output bytes.Buffer
	handler := newDevHandler(&output, slog.LevelInfo)
	logger := slog.New(handler).
		With("application", "memory vault").
		WithGroup("HTTP")
	logger.Info("ready", "status", 200)
	if !hasTabRow(output.String(), "application", "memory vault") ||
		!hasTabRowKey(output.String(), "HTTP") ||
		!strings.Contains(output.String(), "status:200") {
		t.Fatalf("text output = %q", output.String())
	}
}

func TestDevelopmentFormatRequiresDevelopmentMode(t *testing.T) {
	for _, test := range []struct {
		name        string
		development bool
		devFormat   bool
	}{
		{name: "development", development: true, devFormat: true},
		{name: "production"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			var handler slog.Handler
			if test.development {
				handler = newDevHandler(&output, slog.LevelInfo)
			} else {
				handler = slog.NewJSONHandler(&output, &slog.HandlerOptions{
					Level: slog.LevelInfo,
				})
			}
			logger := slog.New(handler)
			logger.Info("hello")
			gotDevFormat := strings.Contains(
				output.String(),
				"\x1b[32mINF\x1b[0m hello",
			)
			if gotDevFormat != test.devFormat {
				t.Fatalf(
					"development format = %v, want %v; output = %q",
					gotDevFormat, test.devFormat, output.String(),
				)
			}
		})
	}
}

func TestDevelopmentSpecialAttrsUseMessageSuffix(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(newDevHandler(&output, slog.LevelInfo)).With(
		System("http"),
		SysOperation("op-123"),
		"request_id", "req-456",
	)
	logger.Info("request", "status", 200)

	got := output.String()
	if !strings.Contains(got, "request [http/op-123] \n") {
		t.Fatalf("output = %q, want message suffix", got)
	}
	if !hasTabRow(got, "request_id", "req-456") ||
		!hasTabRow(got, "status", "200") {
		t.Fatalf("output = %q, want regular attributes", got)
	}
	if strings.Contains(got, systemKey) || strings.Contains(got, sysOperationKey) {
		t.Fatalf("special attrs should not be repeated in pretty JSON: %q", got)
	}
}

func TestProductionSpecialAttrsRemainJSONFields(t *testing.T) {
	t.Setenv("MEMORYD_DEV", "false")
	var output bytes.Buffer
	logger := NewRoot(slog.LevelInfo, &output).With(
		System("api"),
		SysOperation("operation-7"),
	)
	logger.Info("request")

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("logger output is not JSON: %v (%q)", err, output.String())
	}
	if record[systemKey] != "api" || record[sysOperationKey] != "operation-7" {
		t.Fatalf("record = %#v, want reserved fields", record)
	}
}

func TestIsDevelopmentAndWithSupportArbitraryLogger(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil)).With("request_id", "req-1")
	if IsDevelopment(logger) {
		t.Fatal("IsDevelopment returned true for a standard JSON logger")
	}
	if IsDevelopment(nil) {
		t.Fatal("IsDevelopment returned true for a nil logger")
	}
	logger.Info("request")

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("logger output is not JSON: %v (%q)", err, output.String())
	}
	if record["request_id"] != "req-1" {
		t.Fatalf("record = %#v, want request_id attribute", record)
	}
}

func TestForOperationInRequestWithoutRequestIDMatchesForOperation(t *testing.T) {
	t.Setenv("MEMORYD_DEV", "false")

	var operationOutput bytes.Buffer
	operationBase := NewRoot(slog.LevelInfo, &operationOutput).With(System("api"))
	ForOperation(operationBase, "Search").Info(
		"search completed",
		"status",
		200,
	)

	var requestOutput bytes.Buffer
	requestBase := NewRoot(slog.LevelInfo, &requestOutput).With(System("api"))
	ForOperationInRequest(
		requestBase,
		"Search",
		context.Background(),
	).Info("search completed", "status", 200)

	var operationRecord map[string]any
	if err := json.Unmarshal(operationOutput.Bytes(), &operationRecord); err != nil {
		t.Fatalf("operation logger output is not JSON: %v (%q)", err, operationOutput.String())
	}
	var requestRecord map[string]any
	if err := json.Unmarshal(requestOutput.Bytes(), &requestRecord); err != nil {
		t.Fatalf("request logger output is not JSON: %v (%q)", err, requestOutput.String())
	}
	delete(operationRecord, "time")
	delete(requestRecord, "time")

	if !reflect.DeepEqual(requestRecord, operationRecord) {
		t.Fatalf("request record = %#v, want operation record %#v", requestRecord, operationRecord)
	}
	if _, ok := requestRecord["request_id"]; ok {
		t.Fatalf("record unexpectedly contains request_id: %#v", requestRecord)
	}
}

func TestForOperationInRequestAddsRequestIDAsJSONAttribute(t *testing.T) {
	t.Setenv("MEMORYD_DEV", "false")
	var output bytes.Buffer
	base := NewRoot(slog.LevelInfo, &output).With(System("api"))
	ctx := ContextWithRequestID(context.Background(), "req-42")
	ForOperationInRequest(base, "Search", ctx).Info(
		"search completed",
		"status",
		200,
	)

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("logger output is not JSON: %v (%q)", err, output.String())
	}
	if record[systemKey] != "api" || record[sysOperationKey] != "Search" {
		t.Fatalf("record = %#v, want system and operation attributes", record)
	}
	if record["request_id"] != "req-42" || record["status"] != float64(200) ||
		record["msg"] != "search completed" {
		t.Fatalf("record = %#v, want request ID and ordinary JSON fields", record)
	}
	if strings.Count(output.String(), `"`+systemKey+`"`) != 1 ||
		strings.Count(output.String(), `"`+sysOperationKey+`"`) != 1 {
		t.Fatalf("reserved attributes should each appear once: %q", output.String())
	}
}

func TestDevelopmentTabWriterFormatsAttrsAndGroups(t *testing.T) {
	var output bytes.Buffer
	handler := newDevHandler(&output, slog.LevelInfo)
	logger := slog.New(handler).With("application", "memoryd").WithGroup("HTTP")
	logger.Info(
		"request",
		"status",
		200,
		"ok",
		true,
		"latency",
		3667*time.Nanosecond,
	)

	got := output.String()
	if !strings.Contains(got, "request \n") ||
		!hasTabRow(got, "application", "memoryd") ||
		!hasTabRowKey(got, "HTTP") ||
		!strings.Contains(got, "latency:3.667µs") ||
		!strings.Contains(got, "ok:true") ||
		!strings.Contains(got, "status:200") {
		t.Fatalf("text output = %q, want grouped tab output", got)
	}
}

func withoutSourceLine(output string) string {
	lines := strings.Split(output, "\n")
	filtered := lines[:0]
	for _, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "__source ") {
			filtered = append(filtered, line)
		}
	}
	return strings.Join(filtered, "\n")
}

func hasTabRow(output, key, value string) bool {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == key &&
			strings.Join(fields[1:], " ") == value {
			return true
		}
	}
	return false
}

func hasTabRowKey(output, key string) bool {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == key {
			return true
		}
	}
	return false
}

func TestDevelopmentLevelColors(t *testing.T) {
	for _, test := range []struct {
		name  string
		level slog.Level
		want  string
	}{
		{name: "trace", level: slog.Level(-8), want: "\x1b[34mDBG\x1b[0m"},
		{name: "debug", level: slog.LevelDebug, want: "\x1b[34mDBG\x1b[0m"},
		{name: "info", level: slog.LevelInfo, want: "\x1b[32mINF\x1b[0m"},
		{name: "warn", level: slog.LevelWarn, want: "\x1b[33mWRN\x1b[0m"},
		{name: "error", level: slog.LevelError, want: "\x1b[31mERR\x1b[0m"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			handler := newDevHandler(&output, slog.Level(-8))
			record := slog.NewRecord(time.Unix(0, 0), test.level, "message", 0)
			if err := handler.Handle(context.Background(), record); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), test.want) {
				t.Fatalf(
					"output = %q, want level %q",
					output.String(),
					test.want,
				)
			}
		})
	}
}

func TestDevelopmentEnabled(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE", "t"} {
		if !developmentEnabled(value) {
			t.Errorf("developmentEnabled(%q) = false", value)
		}
	}
	for _, value := range []string{"", "0", "false", "no", "development"} {
		if developmentEnabled(value) {
			t.Errorf("developmentEnabled(%q) = true", value)
		}
	}
}
