package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
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
	if !strings.Contains(
		output.String(),
		"DBG  hello {\n    \"answer\": 42\n}",
	) {
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
	want := "2026-09-14 23:57:33 .288000 \x1b[32mINF\x1b[0m  HTTP request {\n" +
		"    \"latency\": \"3.667µs\",\n    \"method\": \"GET\",\n" +
		"    \"path\": \"/docs\",\n    \"status\": 308\n}\n"
	if output.String() != want {
		t.Fatalf("text output = %q, want %q", output.String(), want)
	}
}

func TestDevelopmentHandlerPreservesAttrsAndGroups(t *testing.T) {
	var output bytes.Buffer
	handler := newDevHandler(&output, slog.LevelInfo)
	logger := slog.New(handler).
		With("application", "memory vault").
		WithGroup("HTTP")
	logger.Info("ready", "status", 200)
	if !strings.Contains(
		output.String(),
		"\"HTTP\": {\n        \"status\": 200\n    },\n"+
			"    \"application\": \"memory vault\"",
	) {
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
			handler := newRootHandler("", slog.LevelInfo, &output, test.development)
			logger := slog.New(handler) 
			logger.Info("hello")
			gotDevFormat := strings.Contains(
				output.String(),
				"\x1b[32mINF\x1b[0m  hello",
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

func TestDevelopmentJSONPrettyPrintsAttrsAndGroups(t *testing.T) {
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

	want := "request {\n    \"HTTP\": {\n        \"latency\": \"3.667µs\",\n" +
		"        \"ok\": true,\n        \"status\": 200\n    },\n" +
		"    \"application\": \"memoryd\"\n}\n"
	if !strings.HasSuffix(output.String(), want) {
		t.Fatalf("text output = %q, want suffix %q", output.String(), want)
	}
}

func TestDevelopmentLevelColors(t *testing.T) {
	for _, test := range []struct {
		name  string
		level slog.Level
		want  string
	}{
		{name: "trace", level: slog.Level(-8), want: "\x1b[34mTRC\x1b[0m"},
		{name: "debug", level: slog.LevelDebug, want: " DBG "},
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
