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

	"github.com/anton-povarov/memoryd/server/internal/vault"
)

func TestExternalCommandCapturesPluginExchange(t *testing.T) {
	temporaryDirectory := t.TempDir()
	inputPath := filepath.Join(temporaryDirectory, "note.txt")
	input := []byte("hello from the input file\n")
	if err := os.WriteFile(inputPath, input, 0o600); err != nil {
		t.Fatal(err)
	}

	requestCopyPath := filepath.Join(temporaryDirectory, "plugin-request.json")
	expectedPluginStdout := []byte(strings.ReplaceAll(
		`{"protocol_version":2,"plugin_version":"smoke-2","statistics":{"usage":{"input_tokens":31531,"cached_input_tokens":23040,"cache_write_input_tokens":0,"output_tokens":107,"reasoning_output_tokens":0,"total_tokens":31638}},"cost_estimate":{"amount_usd":0.001,"basis":"standard_api_equivalent_short_context","pricing_date":"2026-09-28"},"artifacts":[{"content_type":"text/markdown","content":"# Smoke result\n<script>alert(1)</script>\nFENCEnested\nliteral\nFENCE\n","provenance":{"method":"smoke test","tool":"fake-plugin"},"scope":{"section":"first"}},{"content_type":"text/plain","content":"Independent words."},{"content_type":"application/json","content":"{\n  \"title\": \"Statement\",\n  \"pages\": [1, 2]\n}","provenance":{"method":"structured extraction","tool":"fake-plugin"}}],"warnings":["limited coverage"]}`,
		"FENCE",
		"```",
	))
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
	for _, stale := range []string{"artifact-099.md", "artifact-100.json", "artifact-101.txt"} {
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

	if !strings.Contains(reportOutput.String(), "\nStatistics:\n") ||
		!strings.Contains(reportOutput.String(), "\nCost estimate:\n") ||
		!strings.Contains(reportOutput.String(), "0.001") {
		t.Fatalf("report omitted structured reporting values: %s", reportOutput.String())
	}

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
	if request.ProtocolVersion != 2 || request.Blob.ByteSize != int64(len(input)) ||
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
	assertDerivedContentFiles(t, captureDirectory, reportOutput.String(), filenames)
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
		DerivedContentFiles []string            `json:"derived_content_files"`
		Statistics          *vault.Statistics   `json:"statistics"`
		CostEstimate        *vault.CostEstimate `json:"cost_estimate"`
		Execution           struct {
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
	if metadata.Statistics == nil || metadata.Statistics.Usage == nil {
		t.Fatalf("captured metadata omitted direct statistics: %s", metadataBytes)
	}
	usage := metadata.Statistics.Usage
	if usage.InputTokens == nil || *usage.InputTokens != 31531 ||
		usage.CachedInputTokens == nil || *usage.CachedInputTokens != 23040 ||
		usage.CacheWriteInputTokens == nil || *usage.CacheWriteInputTokens != 0 ||
		usage.OutputTokens == nil || *usage.OutputTokens != 107 ||
		usage.ReasoningOutputTokens == nil || *usage.ReasoningOutputTokens != 0 ||
		usage.TotalTokens == nil || *usage.TotalTokens != 31638 {
		t.Fatalf("captured statistics changed: %#v", usage)
	}
	if metadata.CostEstimate == nil ||
		metadata.CostEstimate.AmountUSD != 0.001 ||
		metadata.CostEstimate.Basis != "standard_api_equivalent_short_context" ||
		metadata.CostEstimate.PricingDate != "2026-09-28" {
		t.Fatalf("captured direct cost estimate changed: %#v", metadata.CostEstimate)
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

func assertDerivedContentFiles(t *testing.T, captureDirectory, report string, filenames []string) {
	t.Helper()
	if len(filenames) != 3 {
		t.Fatalf("derived content files = %v", filenames)
	}
	wants := []struct {
		name    string
		content string
	}{
		{
			name:    "artifact-001.md",
			content: "# Smoke result\n<script>alert(1)</script>\n```nested\nliteral\n```\n",
		},
		{name: "artifact-002.txt", content: "Independent words."},
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
	literalContentBlock := "````text\n" + wants[0].content + "````\n"
	if !strings.Contains(report, literalContentBlock) {
		t.Fatal("Markdown artifact was not rendered as a literal fenced text block")
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
