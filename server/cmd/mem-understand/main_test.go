package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anton-povarov/memoryd/server/internal/understanding"
)

func TestExternalCommandCapturesPluginExchange(t *testing.T) {
	temporaryDirectory := t.TempDir()
	inputPath := filepath.Join(temporaryDirectory, "note.txt")
	input := []byte("hello from the input file\n")
	if err := os.WriteFile(inputPath, input, 0o600); err != nil {
		t.Fatal(err)
	}

	requestCopyPath := filepath.Join(temporaryDirectory, "plugin-request.json")
	expectedPluginStdout := []byte(
		`{"protocol_version":1,"plugin_version":"smoke-1","usage":{"input_tokens":31531,"cached_input_tokens":23040,"cache_write_input_tokens":0,"output_tokens":107,"reasoning_output_tokens":0,"total_tokens":31638},"turn_usage":[{"turn":2,"usage":{"input_tokens":16000,"cached_input_tokens":10040,"cache_write_input_tokens":0,"output_tokens":50,"reasoning_output_tokens":0,"total_tokens":16050}},{"turn":1,"usage":{"input_tokens":15531,"cached_input_tokens":13000,"cache_write_input_tokens":0,"output_tokens":57,"reasoning_output_tokens":0,"total_tokens":15588}}],"cost_estimate":{"amount_usd":0.001133789,"basis":"standard_api_equivalent_short_context","pricing_date":"2026-09-28","pricing_url":"https://example.com/pricing"},"artifacts":[{"kind":"description","media_type":"text/markdown","content":"# Smoke result\nA readable artifact.","provenance":{"method":"smoke test","tool":"fake-plugin"}},{"kind":"document_text","media_type":"text/markdown","content":"## Other piece\nIndependent words.","provenance":{"method":"separate method","tool":"fake-plugin"}},{"kind":"document_data","media_type":"application/json","content":"{\n  \"title\": \"Statement\",\n  \"pages\": [1, 2]\n}","provenance":{"method":"structured extraction","tool":"fake-plugin"}}],"warnings":["limited coverage"]}`,
	)
	pluginPath := filepath.Join(temporaryDirectory, "fake-plugin")
	pluginScript := "#!/bin/sh\ncat > \"$MEM_UNDERSTAND_REQUEST_COPY\"\nprintf '%s' '" + string(
		expectedPluginStdout,
	) + "'\nprintf '%s' 'plugin diagnostic' >&2\n"
	if err := os.WriteFile(pluginPath, []byte(pluginScript), 0o700); err != nil {
		t.Fatal(err)
	}

	binaryPath := buildMemUnderstand(t, temporaryDirectory)

	captureDirectory := filepath.Join(temporaryDirectory, "captures")
	contentDirectory := filepath.Join(captureDirectory, "derived-content")
	if err := os.MkdirAll(contentDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, stale := range []string{"artifact-099.md", "artifact-100.json"} {
		if err := os.WriteFile(
			filepath.Join(contentDirectory, stale),
			[]byte("stale"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command(
		binaryPath,
		inputPath,
		"--plugin="+pluginPath,
		"--raw-dir="+captureDirectory,
	)
	command.Env = append(os.Environ(), "MEM_UNDERSTAND_REQUEST_COPY="+requestCopyPath)
	var reportOutput bytes.Buffer
	command.Stdout = &reportOutput
	var commandStderr bytes.Buffer
	command.Stderr = &commandStderr
	if err := command.Run(); err != nil {
		t.Fatalf(
			"run mem-understand: %v\nstdout:\n%s\nstderr:\n%s",
			err,
			reportOutput.String(),
			commandStderr.String(),
		)
	}
	if !bytes.Equal(commandStderr.Bytes(), []byte("plugin diagnostic")) {
		t.Fatalf(
			"streamed stderr = %q, want exactly %q",
			commandStderr.Bytes(),
			"plugin diagnostic",
		)
	}

	assertReadableReport(t, reportOutput.String())

	requestBytes, err := os.ReadFile(filepath.Join(captureDirectory, "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	pluginRequestBytes, err := os.ReadFile(requestCopyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(requestBytes, pluginRequestBytes) {
		t.Fatal("saved request bytes differ from the bytes received by the plugin")
	}
	var request struct {
		ProtocolVersion int `json:"protocol_version"`
		Blob            struct {
			Blobref       string `json:"blobref"`
			MediaType     string `json:"media_type"`
			ByteSize      int64  `json:"byte_size"`
			ContentBase64 string `json:"content_base64"`
		} `json:"blob"`
		ImportContext struct {
			OriginalFilename string `json:"original_filename"`
		} `json:"import_context"`
	}
	if err := json.Unmarshal(requestBytes, &request); err != nil {
		t.Fatalf("decode captured request: %v", err)
	}
	decodedContent, err := base64.StdEncoding.DecodeString(request.Blob.ContentBase64)
	if err != nil {
		t.Fatalf("decode captured Blob: %v", err)
	}
	if request.ProtocolVersion != 1 || request.Blob.ByteSize != int64(len(input)) ||
		request.Blob.MediaType != "text/plain" || !bytes.Equal(decodedContent, input) ||
		request.Blob.Blobref == "" || request.ImportContext.OriginalFilename != "note.txt" {
		t.Fatalf("captured request does not describe imported bytes and context: %#v", request)
	}

	stdoutBytes, err := os.ReadFile(filepath.Join(captureDirectory, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stdoutBytes, expectedPluginStdout) {
		t.Fatalf(
			"captured stdout = %q, want exact plugin output %q",
			stdoutBytes,
			expectedPluginStdout,
		)
	}
	stderrBytes, err := os.ReadFile(filepath.Join(captureDirectory, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stderrBytes) != "plugin diagnostic" {
		t.Fatalf("captured stderr = %q", stderrBytes)
	}
	filenames := assertCaptureMetadata(t, captureDirectory)
	assertDerivedContentFiles(
		t,
		captureDirectory,
		reportOutput.String(),
		filenames,
	)
	reportBytes, err := os.ReadFile(filepath.Join(captureDirectory, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reportBytes, reportOutput.Bytes()) {
		t.Fatal("saved report differs from the displayed report")
	}
}

func TestExternalCommandStreamsAndCapturesStderrOnPluginFailure(t *testing.T) {
	temporaryDirectory := t.TempDir()
	inputPath := filepath.Join(temporaryDirectory, "note.txt")
	if err := os.WriteFile(inputPath, []byte("failure test input\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pluginPath := filepath.Join(temporaryDirectory, "failing-plugin")
	pluginStderr := []byte("plugin diagnostic before failure\n")
	releasePath := filepath.Join(temporaryDirectory, "release-plugin")
	pluginScript := "#!/bin/sh\nprintf '%s' '" + string(pluginStderr) +
		"' >&2\nwhile [ ! -e \"$MEM_UNDERSTAND_RELEASE\" ]; do sleep 0.01; done\nexit 23\n"
	if err := os.WriteFile(pluginPath, []byte(pluginScript), 0o700); err != nil {
		t.Fatal(err)
	}

	binaryPath := buildMemUnderstand(t, temporaryDirectory)
	captureDirectory := filepath.Join(temporaryDirectory, "captures")
	command := exec.Command(
		binaryPath,
		inputPath,
		"--plugin="+pluginPath,
		"--raw-dir="+captureDirectory,
	)
	command.Env = append(os.Environ(), "MEM_UNDERSTAND_RELEASE="+releasePath)
	var reportOutput bytes.Buffer
	command.Stdout = &reportOutput
	stderrPipe, err := command.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	released := false
	releasePlugin := func() {
		if !released {
			released = true
			if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
				t.Errorf("release test plugin: %v", err)
			}
		}
	}
	defer releasePlugin()
	type stderrResult struct {
		bytes []byte
		err   error
	}
	firstStderr := make(chan struct{}, 1)
	stderrDone := make(chan stderrResult, 1)
	go func() {
		var streamed bytes.Buffer
		buffer := make([]byte, 1024)
		for {
			count, readErr := stderrPipe.Read(buffer)
			if count > 0 {
				_, _ = streamed.Write(buffer[:count])
				select {
				case firstStderr <- struct{}{}:
				default:
				}
			}
			if readErr != nil {
				stderrDone <- stderrResult{bytes: append([]byte(nil), streamed.Bytes()...), err: readErr}
				return
			}
		}
	}()
	streamedBeforeExit := false
	streamTimer := time.NewTimer(5 * time.Second)
	select {
	case <-firstStderr:
		streamedBeforeExit = true
	case <-streamTimer.C:
	}
	if !streamTimer.Stop() {
		select {
		case <-streamTimer.C:
		default:
		}
	}
	releasePlugin()
	stderrOutput := <-stderrDone
	if stderrOutput.err != nil && stderrOutput.err != io.EOF {
		t.Fatalf("read command stderr: %v", stderrOutput.err)
	}
	err = command.Wait()
	if err == nil {
		t.Fatal("run mem-understand succeeded for a plugin that exited with status 23")
	} else if exitError, ok := err.(*exec.ExitError); !ok || exitError.ExitCode() != exitFailure {
		t.Fatalf("run mem-understand error = %v, want exit status %d", err, exitFailure)
	}
	if !streamedBeforeExit {
		t.Fatal("plugin stderr was not streamed while the plugin process was still running")
	}
	if !bytes.Equal(stderrOutput.bytes, pluginStderr) {
		t.Fatalf("streamed stderr = %q, want exactly %q", stderrOutput.bytes, pluginStderr)
	}
	capturedStderr, err := os.ReadFile(filepath.Join(captureDirectory, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(capturedStderr, pluginStderr) {
		t.Fatalf("captured stderr = %q, want exact plugin bytes %q", capturedStderr, pluginStderr)
	}
	metadataBytes, err := os.ReadFile(filepath.Join(captureDirectory, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		Execution struct {
			ExitCode      int    `json:"exit_code"`
			ProcessStatus string `json:"process_status"`
		} `json:"execution"`
	}
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		t.Fatalf("decode execution metadata: %v", err)
	}
	if metadata.Execution.ExitCode != 23 || metadata.Execution.ProcessStatus != "failed" {
		t.Fatalf("plugin exit outcome was not preserved: %s", metadataBytes)
	}
}

func buildMemUnderstand(t *testing.T, temporaryDirectory string) string {
	t.Helper()
	binaryPath := filepath.Join(temporaryDirectory, "mem-understand")
	build := exec.Command("go", "build", "-o", binaryPath, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build mem-understand: %v\n%s", err, output)
	}
	return binaryPath
}

func assertCaptureMetadata(t *testing.T, captureDirectory string) []string {
	t.Helper()
	metadataBytes, err := os.ReadFile(filepath.Join(captureDirectory, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		DerivedContentFiles []string `json:"derived_content_files"`
		Usage               struct {
			InputTokens           uint64  `json:"input_tokens"`
			CachedInputTokens     uint64  `json:"cached_input_tokens"`
			CacheWriteInputTokens *uint64 `json:"cache_write_input_tokens"`
			OutputTokens          uint64  `json:"output_tokens"`
			ReasoningOutputTokens uint64  `json:"reasoning_output_tokens"`
			TotalTokens           uint64  `json:"total_tokens"`
		} `json:"usage"`
		TurnUsage []struct {
			Turn  uint64 `json:"turn"`
			Usage struct {
				InputTokens           uint64  `json:"input_tokens"`
				CachedInputTokens     uint64  `json:"cached_input_tokens"`
				CacheWriteInputTokens *uint64 `json:"cache_write_input_tokens"`
				OutputTokens          uint64  `json:"output_tokens"`
				ReasoningOutputTokens uint64  `json:"reasoning_output_tokens"`
				TotalTokens           uint64  `json:"total_tokens"`
			} `json:"usage"`
		} `json:"turn_usage"`
		CostEstimate struct {
			AmountUSD   float64 `json:"amount_usd"`
			Basis       string  `json:"basis"`
			PricingDate string  `json:"pricing_date"`
			PricingURL  string  `json:"pricing_url"`
		} `json:"cost_estimate"`
		Execution struct {
			ExitCode       int    `json:"exit_code"`
			ProcessStatus  string `json:"process_status"`
			ProtocolStatus string `json:"protocol_status"`
		} `json:"execution"`
	}
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		t.Fatalf("decode execution metadata: %v", err)
	}
	if metadata.Execution.ExitCode != 0 ||
		metadata.Execution.ProcessStatus != "exited successfully" ||
		metadata.Execution.ProtocolStatus != "valid" {
		t.Fatalf("unexpected execution metadata: %s", metadataBytes)
	}
	if metadata.Usage.InputTokens != 31531 || metadata.Usage.CachedInputTokens != 23040 ||
		metadata.Usage.CacheWriteInputTokens == nil || *metadata.Usage.CacheWriteInputTokens != 0 ||
		metadata.Usage.OutputTokens != 107 || metadata.Usage.ReasoningOutputTokens != 0 ||
		metadata.Usage.TotalTokens != 31638 {
		t.Fatalf("unexpected captured usage: %s", metadataBytes)
	}
	if len(metadata.TurnUsage) != 2 || metadata.TurnUsage[0].Turn != 2 ||
		metadata.TurnUsage[0].Usage.InputTokens != 16000 || metadata.TurnUsage[1].Turn != 1 ||
		metadata.TurnUsage[1].Usage.InputTokens != 15531 {
		t.Fatalf("unexpected captured turn usage: %s", metadataBytes)
	}
	if metadata.CostEstimate.AmountUSD != 0.001133789 ||
		metadata.CostEstimate.Basis != "standard_api_equivalent_short_context" ||
		metadata.CostEstimate.PricingDate != "2026-09-28" ||
		metadata.CostEstimate.PricingURL != "https://example.com/pricing" {
		t.Fatalf("unexpected captured cost estimate: %s", metadataBytes)
	}
	return metadata.DerivedContentFiles
}

func TestParseModelSettingsBuildRequest(t *testing.T) {
	temporaryDirectory := t.TempDir()
	inputPath := filepath.Join(temporaryDirectory, "note.txt")
	if err := os.WriteFile(inputPath, []byte("model request input\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	parsed, err := parseArgs([]string{
		inputPath,
		"--plugin=/absolute/path/to/plugin",
		"--model-provider", "codex_app_server",
		"--model-name=gpt-6-luna",
		"--model-effort", "medium",
		"--app-server-command=" + filepath.Join(temporaryDirectory, "codex"),
	})
	if err != nil {
		t.Fatalf("parse model settings: %v", err)
	}
	if parsed.model == nil {
		t.Fatal("model settings were not retained")
	}
	var metadata captureMetadata
	var requestBytes []byte
	if err := prepareRequest(inputPath, parsed.model, &metadata, &requestBytes); err != nil {
		t.Fatalf("prepare request: %v", err)
	}
	var request struct {
		Model struct {
			Provider        string   `json:"provider"`
			Name            string   `json:"name"`
			ReasoningEffort string   `json:"reasoning_effort"`
			Command         []string `json:"command"`
		} `json:"model"`
	}
	if err := json.Unmarshal(requestBytes, &request); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	wantCommand := []string{filepath.Join(temporaryDirectory, "codex"), "app-server", "--stdio"}
	if request.Model.Provider != "codex_app_server" || request.Model.Name != "gpt-6-luna" ||
		request.Model.ReasoningEffort != "medium" || len(request.Model.Command) != len(wantCommand) {
		t.Fatalf("request model = %#v", request.Model)
	}
	for index, argument := range wantCommand {
		if request.Model.Command[index] != argument {
			t.Fatalf("request command = %v, want %v", request.Model.Command, wantCommand)
		}
	}
}

func TestParseModelSettingsMustBeCompleteAndSafe(t *testing.T) {
	base := []string{"input.txt", "--plugin=/absolute/path/to/plugin"}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "incomplete settings",
			args: append(append([]string{}, base...), "--model-provider=codex_app_server"),
			want: "model settings require",
		},
		{
			name: "relative app server executable",
			args: append(append([]string{}, base...),
				"--model-provider=codex_app_server", "--model-name=gpt-6-luna",
				"--model-effort=medium", "--app-server-command=codex"),
			want: "absolute executable",
		},
		{
			name: "unsupported provider",
			args: append(append([]string{}, base...),
				"--model-provider=ollama", "--model-name=model", "--model-effort=medium",
				"--app-server-command=/usr/local/bin/codex"),
			want: "codex_app_server",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseArgs(test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parseArgs error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func assertReadableReport(t *testing.T, report string) {
	t.Helper()
	for _, expected := range []string{
		"# Smoke result", "## Other piece", "limited coverage", `"method": "smoke test"`,
		"### JSON content", "```json\n{\n  \"title\": \"Statement\"",
		"- Input tokens: 31531", "- Cached input tokens: 23040", "- Cache write input tokens: 0",
		"- Reasoning output tokens: 0", "- Total tokens: 31638",
		"### Turn 1", "### Turn 2", "### Cumulative total",
		"- Input tokens: 15531", "- Input tokens: 16000",
		"API-equivalent estimate (not an actual subscription charge): $0.001133789 USD",
		"`standard_api_equivalent_short_context`", "2026-09-28", "https://example.com/pricing",
		"does not represent an actual subscription charge",
	} {
		if !strings.Contains(report, expected) {
			t.Fatalf("command report omitted %q:\n%s", expected, report)
		}
	}
}

func TestRenderReportKeepsLegacyUsageDisplayWithoutTurnBreakdown(t *testing.T) {
	usage := understanding.Usage{
		InputTokens:           4,
		CachedInputTokens:     1,
		OutputTokens:          2,
		ReasoningOutputTokens: 1,
		TotalTokens:           6,
	}
	result := &understanding.Result{
		PluginVersion: "test-1",
		Usage:         &usage,
	}
	report := renderReport(captureMetadata{}, result)
	for _, expected := range []string{
		"## Usage", "- Input tokens: 4", "- Cached input tokens: 1", "- Output tokens: 2",
		"- Reasoning output tokens: 1", "- Total tokens: 6",
	} {
		if !strings.Contains(report, expected) {
			t.Fatalf("legacy usage report omitted %q:\n%s", expected, report)
		}
	}
	if strings.Contains(report, "Turn ") || strings.Contains(report, "Cumulative total") {
		t.Fatalf("legacy usage report unexpectedly includes turn headings:\n%s", report)
	}
}

func TestRenderReportOmitsEmptyTurnUsageHeading(t *testing.T) {
	report := renderReport(captureMetadata{}, &understanding.Result{
		PluginVersion: "test-1",
		TurnUsage:     []understanding.TurnUsageEntry{},
	})
	if strings.Contains(report, "## Usage") {
		t.Fatalf("empty turn usage rendered an empty usage section:\n%s", report)
	}
}

func assertDerivedContentFiles(t *testing.T, captureDirectory, report string, filenames []string) {
	t.Helper()
	if len(filenames) != 3 {
		t.Fatalf("derived content files = %v", filenames)
	}
	wants := []struct {
		name    string
		content string
	}{
		{name: "artifact-001.md", content: "# Smoke result\nA readable artifact."},
		{name: "artifact-002.md", content: "## Other piece\nIndependent words."},
		{
			name:    "artifact-003.json",
			content: "{\n  \"title\": \"Statement\",\n  \"pages\": [1, 2]\n}",
		},
	}
	for index := range wants {
		path := filepath.Join(captureDirectory, "derived-content", wants[index].name)
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != wants[index].content || filenames[index] != path ||
			!strings.Contains(report, path) {
			t.Fatalf("derived content %d was not saved independently", index+1)
		}
	}
	entries, err := os.ReadDir(filepath.Join(captureDirectory, "derived-content"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(wants) {
		t.Fatalf("derived content directory retained stale files: %v", entries)
	}
	for index, want := range wants {
		if entries[index].Name() != want.name {
			t.Fatalf(
				"derived content directory entry %d = %q, want %q",
				index,
				entries[index].Name(),
				want.name,
			)
		}
	}
}
