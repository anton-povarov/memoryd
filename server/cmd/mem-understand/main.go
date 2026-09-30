package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anton-povarov/memoryd/server/internal/understanding"
	"github.com/anton-povarov/memoryd/server/internal/vault"
)

const (
	exitFailure = 1
	exitUsage   = 2
)

var captureFiles = []string{"request.json", "stdout", "stderr", "metadata.json", "report.md"}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	options, err := parseArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}

	inputPath, err := filepath.Abs(options.inputPath)
	if err != nil {
		fmt.Fprintf(stderr, "resolve input path: %v\n", err)
		return exitFailure
	}
	captureDir, err := prepareCaptureDir(options.rawDir)
	if err != nil {
		fmt.Fprintf(stderr, "prepare capture directory: %v\n", err)
		return exitFailure
	}

	metadata := captureMetadata{
		Plugin:           options.plugin,
		Input:            inputMetadata{File: inputPath},
		Captures:         captureFiles,
		CaptureDirectory: captureDir,
		Execution: executionMetadata{
			ProcessStatus:  "not started",
			ProtocolStatus: "not checked",
		},
	}
	var requestBytes []byte
	var stdoutBytes, stderrBytes []byte
	var result *understanding.Result
	var failure string
	if err := prepareRequest(inputPath, options.model, &metadata, &requestBytes); err != nil {
		failure = err.Error()
	}
	if failure == "" {
		execution := understanding.Run(
			context.Background(),
			[]string{options.plugin},
			requestBytes,
			stderr,
		)
		stdoutBytes = execution.Stdout
		stderrBytes = execution.Stderr
		metadata.Execution = executionMetadata{
			StartedAt:      execution.StartedAt,
			CompletedAt:    execution.CompletedAt,
			Duration:       execution.Duration.String(),
			ExitCode:       execution.ExitCode,
			ProcessError:   execution.ProcessError,
			ProcessStatus:  processStatus(execution),
			ProtocolStatus: "not checked",
		}
		switch {
		case execution.ProcessError != "":
			failure = execution.ProcessError
		case execution.ExitCode == nil || *execution.ExitCode != 0:
			failure = "plugin process did not exit successfully"
		default:
			validated, validationErr := understanding.ValidateResult(stdoutBytes)
			if validationErr != nil {
				metadata.Execution.ProtocolStatus = "invalid"
				metadata.Execution.ProtocolError = validationErr.Error()
				failure = "invalid plugin result: " + validationErr.Error()
			} else {
				result = &validated
				metadata.Execution.ProtocolStatus = "valid"
				metadata.PluginVersion = validated.PluginVersion
				metadata.ArtifactCount = len(validated.Artifacts)
				metadata.WarningCount = len(validated.Warnings)
				metadata.Usage = validated.Usage
				metadata.TurnUsage = validated.TurnUsage
				metadata.CostEstimate = validated.CostEstimate
			}
		}
	}
	if result != nil {
		for index := range result.Artifacts {
			metadata.DerivedContentFiles = append(
				metadata.DerivedContentFiles,
				filepath.Join(
					captureDir,
					"derived-content",
					derivedContentFilename(index+1, result.Artifacts[index]),
				),
			)
		}
	}
	metadata.Failure = failure
	report := renderReport(metadata, result)
	if err := writeCaptures(
		captureDir,
		requestBytes,
		stdoutBytes,
		stderrBytes,
		metadata,
		report,
		result,
	); err != nil {
		fmt.Fprintf(stderr, "save plugin captures in %s: %v\n", captureDir, err)
		return exitFailure
	}
	_, _ = io.WriteString(stdout, report)
	if failure != "" {
		return exitFailure
	}
	return 0
}

type options struct {
	inputPath        string
	plugin           string
	rawDir           string
	modelProvider    string
	modelName        string
	modelEffort      string
	appServerCommand string
	model            *understanding.RequestModel
}

func parseArgs(args []string) (options, error) {
	var parsed options
	var positional []string
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--" {
			positional = append(positional, args[index+1:]...)
			break
		}
		name, value, hasValue := strings.Cut(argument, "=")
		switch name {
		case "--plugin", "--raw-dir", "--model-provider", "--model-name",
			"--model-effort", "--app-server-command":
		default:
			if strings.HasPrefix(argument, "-") {
				return options{}, fmt.Errorf("unknown option %q", argument)
			}
			positional = append(positional, argument)
			continue
		}
		if !hasValue {
			index++
			if index >= len(args) {
				return options{}, fmt.Errorf("%s requires a value", name)
			}
			value = args[index]
		}
		if value == "" {
			return options{}, fmt.Errorf("%s must not be empty", name)
		}
		switch name {
		case "--plugin":
			if parsed.plugin != "" {
				return options{}, errors.New("--plugin may be supplied only once")
			}
			parsed.plugin = value
		case "--raw-dir":
			if parsed.rawDir != "" {
				return options{}, errors.New("--raw-dir may be supplied only once")
			}
			parsed.rawDir = value
		case "--model-provider":
			if parsed.modelProvider != "" {
				return options{}, errors.New("--model-provider may be supplied only once")
			}
			parsed.modelProvider = value
		case "--model-name":
			if parsed.modelName != "" {
				return options{}, errors.New("--model-name may be supplied only once")
			}
			parsed.modelName = value
		case "--model-effort":
			if parsed.modelEffort != "" {
				return options{}, errors.New("--model-effort may be supplied only once")
			}
			parsed.modelEffort = value
		case "--app-server-command":
			if parsed.appServerCommand != "" {
				return options{}, errors.New("--app-server-command may be supplied only once")
			}
			parsed.appServerCommand = value
		}
	}
	if parsed.plugin == "" {
		return options{}, errors.New(usage())
	}
	if !filepath.IsAbs(parsed.plugin) {
		return options{}, errors.New("--plugin must be an absolute executable path")
	}
	if len(positional) != 1 {
		return options{}, errors.New(usage())
	}
	modelSettingCount := 0
	for _, setting := range []string{
		parsed.modelProvider, parsed.modelName, parsed.modelEffort, parsed.appServerCommand,
	} {
		if setting != "" {
			modelSettingCount++
		}
	}
	if modelSettingCount != 0 && modelSettingCount != 4 {
		return options{}, errors.New(
			"model settings require --model-provider, --model-name, --model-effort, and --app-server-command",
		)
	}
	if modelSettingCount == 4 {
		model := understanding.RequestModel{
			Provider:        parsed.modelProvider,
			Name:            parsed.modelName,
			ReasoningEffort: parsed.modelEffort,
			Command:         []string{parsed.appServerCommand, "app-server", "--stdio"},
		}
		if err := model.Validate(); err != nil {
			return options{}, fmt.Errorf("invalid model settings: %w", err)
		}
		parsed.model = &model
	}
	parsed.inputPath = positional[0]
	return parsed, nil
}

func usage() string {
	return "usage: mem-understand INPUT --plugin=/absolute/path/to/plugin [--raw-dir=DIR] " +
		"[--model-provider=codex_app_server --model-name=NAME --model-effort=EFFORT " +
		"--app-server-command=/absolute/path/to/codex]"
}

func prepareCaptureDir(rawDir string) (string, error) {
	if rawDir == "" {
		return os.MkdirTemp("", "mem-understand-")
	}
	directory, err := filepath.Abs(rawDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	return directory, nil
}

func prepareRequest(
	inputPath string,
	model *understanding.RequestModel,
	metadata *captureMetadata,
	requestBytes *[]byte,
) error {
	file, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("read input file: %w", err)
	}
	info, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return fmt.Errorf("inspect input file: %w", statErr)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return errors.New("input must be a regular file")
	}
	if info.Size() > vault.MaxBlobBytes {
		_ = file.Close()
		return vault.ErrBlobTooLarge
	}
	content, readErr := io.ReadAll(io.LimitReader(file, vault.MaxBlobBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return fmt.Errorf("read input file: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close input file: %w", closeErr)
	}
	if int64(len(content)) > vault.MaxBlobBytes {
		return vault.ErrBlobTooLarge
	}
	metadata.Input.ByteSize = int64(len(content))
	modifiedAt := info.ModTime().UTC()
	importContext := vault.ImportContext{
		OriginalFilename:   filepath.Base(inputPath),
		FullPath:           inputPath,
		FilesystemModified: &modifiedAt,
	}
	mediaType, err := detectMediaType(content)
	if err != nil {
		return err
	}
	metadata.Input.MediaType = mediaType
	normalizedContext, err := importContext.Normalized()
	if err != nil {
		return fmt.Errorf("normalize Import Context: %w", err)
	}
	var request understanding.Request
	var encodedRequest []byte
	if model == nil {
		request, encodedRequest, err = understanding.BuildRequest(
			content, mediaType, normalizedContext,
		)
	} else {
		request, encodedRequest, err = understanding.BuildRequest(
			content, mediaType, normalizedContext, *model,
		)
	}
	if err != nil {
		return err
	}
	metadata.Input.Blobref = request.Blob.Blobref
	metadata.Input.ByteSize = request.Blob.ByteSize
	metadata.Input.ImportContext = request.ImportContext
	*requestBytes = encodedRequest
	return nil
}

func detectMediaType(content []byte) (string, error) {
	file, err := os.CreateTemp("", "mem-understand-media-*")
	if err != nil {
		return "", fmt.Errorf("stage input for Media Type detection: %w", err)
	}
	path := file.Name()
	defer func() { _ = os.Remove(path) }()
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("stage input for Media Type detection: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close staged input for Media Type detection: %w", err)
	}
	mediaType, err := vault.ResolveMediaType(path, "")
	if err != nil {
		return "", fmt.Errorf("resolve input Media Type: %w", err)
	}
	return mediaType, nil
}

func processStatus(execution understanding.Execution) string {
	if execution.ProcessError != "" {
		return "failed"
	}
	if execution.ExitCode != nil && *execution.ExitCode == 0 {
		return "exited successfully"
	}
	if execution.ExitCode != nil {
		return fmt.Sprintf("exited with status %d", *execution.ExitCode)
	}
	return "unknown"
}

type captureMetadata struct {
	Plugin              string                         `json:"plugin"`
	PluginVersion       string                         `json:"plugin_version,omitempty"`
	Usage               *understanding.Usage           `json:"usage,omitempty"`
	TurnUsage           []understanding.TurnUsageEntry `json:"turn_usage,omitempty"`
	CostEstimate        *understanding.CostEstimate    `json:"cost_estimate,omitempty"`
	Input               inputMetadata                  `json:"input"`
	CaptureDirectory    string                         `json:"capture_directory"`
	Captures            []string                       `json:"captures"`
	DerivedContentFiles []string                       `json:"derived_content_files,omitempty"`
	Execution           executionMetadata              `json:"execution"`
	ArtifactCount       int                            `json:"artifact_count,omitempty"`
	WarningCount        int                            `json:"warning_count,omitempty"`
	Failure             string                         `json:"failure,omitempty"`
}

type inputMetadata struct {
	File          string                             `json:"file"`
	Blobref       string                             `json:"blobref,omitempty"`
	ByteSize      int64                              `json:"byte_size"`
	MediaType     string                             `json:"media_type,omitempty"`
	ImportContext understanding.RequestImportContext `json:"import_context"`
}

type executionMetadata struct {
	StartedAt      time.Time `json:"started_at,omitempty"`
	CompletedAt    time.Time `json:"completed_at,omitempty"`
	Duration       string    `json:"duration,omitempty"`
	ExitCode       *int      `json:"exit_code,omitempty"`
	ProcessError   string    `json:"process_error,omitempty"`
	ProcessStatus  string    `json:"process_status"`
	ProtocolStatus string    `json:"protocol_status"`
	ProtocolError  string    `json:"protocol_error,omitempty"`
}

func writeCaptures(
	directory string,
	requestBytes, stdoutBytes, stderrBytes []byte,
	metadata captureMetadata,
	report string,
	result *understanding.Result,
) error {
	contents := map[string][]byte{
		"request.json": requestBytes,
		"stdout":       stdoutBytes,
		"stderr":       stderrBytes,
	}
	for _, name := range captureFiles[:3] {
		if err := os.WriteFile(filepath.Join(directory, name), contents[name], 0o600); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	if err := writeDerivedContent(directory, metadata.DerivedContentFiles, result); err != nil {
		return err
	}
	metadataJSON, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("encode execution metadata: %w", err)
	}
	metadataJSON = append(metadataJSON, '\n')
	if err := os.WriteFile(
		filepath.Join(directory, "metadata.json"),
		metadataJSON,
		0o600,
	); err != nil {
		return fmt.Errorf("write metadata.json: %w", err)
	}
	if err := os.WriteFile(
		filepath.Join(directory, "report.md"),
		[]byte(report),
		0o600,
	); err != nil {
		return fmt.Errorf("write report.md: %w", err)
	}
	return nil
}

func writeDerivedContent(
	directory string,
	filenames []string,
	result *understanding.Result,
) error {
	contentDir := filepath.Join(directory, "derived-content")
	entries, err := os.ReadDir(contentDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read derived content directory: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "artifact-") &&
			(strings.HasSuffix(entry.Name(), ".md") || strings.HasSuffix(entry.Name(), ".json")) {
			if err := os.Remove(filepath.Join(contentDir, entry.Name())); err != nil {
				return fmt.Errorf("remove earlier derived content file: %w", err)
			}
		}
	}
	if result == nil || len(result.Artifacts) == 0 {
		return nil
	}
	if err := os.MkdirAll(contentDir, 0o700); err != nil {
		return fmt.Errorf("create derived content directory: %w", err)
	}
	for index, artifact := range result.Artifacts {
		if err := os.WriteFile(filenames[index], []byte(artifact.Content), 0o600); err != nil {
			return fmt.Errorf("write derived content %d: %w", index+1, err)
		}
	}
	return nil
}

func derivedContentFilename(index int, artifact understanding.Artifact) string {
	extension := ".md"
	if artifact.MediaType == understanding.JSONMediaType {
		extension = ".json"
	}
	return fmt.Sprintf("artifact-%03d%s", index, extension)
}

func renderReport(metadata captureMetadata, result *understanding.Result) string {
	var report strings.Builder
	_, _ = fmt.Fprintf(&report, "# Understanding Plugin Report\n\n")
	_, _ = fmt.Fprintf(&report, "- Plugin: `%s`\n", metadata.Plugin)
	_, _ = fmt.Fprintf(&report, "- Input: `%s`\n", metadata.Input.File)
	if metadata.Input.Blobref != "" {
		_, _ = fmt.Fprintf(&report, "- Blobref: `%s`\n", metadata.Input.Blobref)
		_, _ = fmt.Fprintf(&report, "- Byte size: %d\n", metadata.Input.ByteSize)
		_, _ = fmt.Fprintf(&report, "- Media Type: `%s`\n", metadata.Input.MediaType)
		_, _ = fmt.Fprintf(
			&report,
			"- Import Context: `%s`\n",
			metadata.Input.ImportContext.OriginalFilename,
		)
	}
	_, _ = fmt.Fprintf(&report, "- Process: %s\n", metadata.Execution.ProcessStatus)
	if metadata.Execution.ExitCode != nil {
		_, _ = fmt.Fprintf(&report, "- Exit code: %d\n", *metadata.Execution.ExitCode)
	}
	if metadata.Execution.ProcessError != "" {
		_, _ = fmt.Fprintf(&report, "- Execution error: %s\n", metadata.Execution.ProcessError)
	}
	_, _ = fmt.Fprintf(&report, "- Protocol: %s\n", metadata.Execution.ProtocolStatus)
	if metadata.Execution.ProtocolError != "" {
		_, _ = fmt.Fprintf(&report, "- Protocol error: %s\n", metadata.Execution.ProtocolError)
	}
	_, _ = fmt.Fprintf(&report, "- Captures: `%s`\n", metadata.CaptureDirectory)
	if metadata.Failure != "" {
		_, _ = fmt.Fprintf(&report, "\nFailure: %s\n", metadata.Failure)
	}
	if result == nil {
		return report.String()
	}

	_, _ = fmt.Fprintf(&report, "\nPlugin version: `%s`\n", result.PluginVersion)
	if result.Usage != nil || len(result.TurnUsage) > 0 {
		_, _ = fmt.Fprintf(&report, "\n## Usage\n\n")
		if len(result.TurnUsage) > 0 {
			turns := append([]understanding.TurnUsageEntry(nil), result.TurnUsage...)
			sort.Slice(turns, func(left, right int) bool {
				return turns[left].Turn < turns[right].Turn
			})
			for index, turn := range turns {
				if index > 0 {
					_, _ = fmt.Fprintln(&report)
				}
				_, _ = fmt.Fprintf(&report, "### Turn %d\n\n", turn.Turn)
				renderUsageCounts(&report, turn.Usage)
			}
		}
		if result.Usage != nil {
			if len(result.TurnUsage) > 0 {
				_, _ = fmt.Fprintf(&report, "\n### Cumulative total\n\n")
			}
			renderUsageCounts(&report, *result.Usage)
		}
	}
	if result.CostEstimate != nil {
		estimate := result.CostEstimate
		label := "Cost estimate (not an actual subscription charge)"
		if strings.HasPrefix(estimate.Basis, "standard_api_equivalent") {
			label = "API-equivalent estimate (not an actual subscription charge)"
		}
		_, _ = fmt.Fprintf(&report, "\n## Cost estimate\n\n")
		_, _ = fmt.Fprintf(
			&report,
			"- %s: $%s USD\n",
			label,
			strconv.FormatFloat(estimate.AmountUSD, 'f', -1, 64),
		)
		_, _ = fmt.Fprintf(&report, "- Basis: `%s`\n", estimate.Basis)
		_, _ = fmt.Fprintf(&report, "- Pricing date: %s\n", estimate.PricingDate)
		_, _ = fmt.Fprintf(&report, "- Pricing source: <%s>\n", estimate.PricingURL)
		if strings.HasPrefix(estimate.Basis, "standard_api_equivalent") {
			_, _ = fmt.Fprintf(
				&report,
				"\nThis API-equivalent estimate uses the plugin-reported pricing basis and does not represent an actual subscription charge.\n",
			)
		} else {
			_, _ = fmt.Fprintf(
				&report,
				"\nThis estimate uses the plugin-reported pricing basis and does not represent an actual subscription charge.\n",
			)
		}
	}
	if len(result.Warnings) > 0 {
		_, _ = fmt.Fprintf(&report, "\n## Warnings\n\n")
		for _, warning := range result.Warnings {
			_, _ = fmt.Fprintf(&report, "- %s\n", warning)
		}
	}
	if len(result.Artifacts) == 0 {
		_, _ = fmt.Fprintf(&report, "\nNo artifacts returned.\n")
	}
	for index, artifact := range result.Artifacts {
		_, _ = fmt.Fprintf(&report, "\n## Artifact %d: `%s`\n\n", index+1, artifact.Kind)
		_, _ = fmt.Fprintf(&report, "Media Type: `%s`\n\n", artifact.MediaType)
		_, _ = fmt.Fprintf(
			&report,
			"Saved content: `%s`\n\nProvenance:\n",
			metadata.DerivedContentFiles[index],
		)
		if len(artifact.Provenance) == 0 {
			_, _ = fmt.Fprintf(&report, "\n(empty)\n")
		} else {
			provenance, _ := json.MarshalIndent(artifact.Provenance, "", "  ")
			_, _ = fmt.Fprintf(&report, "\n```json\n%s\n```\n", provenance)
		}
		if artifact.MediaType == understanding.JSONMediaType {
			_, _ = fmt.Fprintf(&report, "\n### JSON content\n\n```json\n%s", artifact.Content)
			if !strings.HasSuffix(artifact.Content, "\n") {
				_, _ = fmt.Fprintln(&report)
			}
			_, _ = fmt.Fprintln(&report, "```")
		} else {
			_, _ = fmt.Fprintf(&report, "\n### Markdown content\n\n%s", artifact.Content)
			if !strings.HasSuffix(artifact.Content, "\n") {
				_, _ = fmt.Fprintln(&report)
			}
		}
	}
	return report.String()
}

func renderUsageCounts(report *strings.Builder, usage understanding.Usage) {
	_, _ = fmt.Fprintf(report, "- Input tokens: %d\n", usage.InputTokens)
	_, _ = fmt.Fprintf(report, "- Cached input tokens: %d\n", usage.CachedInputTokens)
	if usage.CacheWriteInputTokens != nil {
		_, _ = fmt.Fprintf(report, "- Cache write input tokens: %d\n", *usage.CacheWriteInputTokens)
	}
	_, _ = fmt.Fprintf(report, "- Output tokens: %d\n", usage.OutputTokens)
	_, _ = fmt.Fprintf(report, "- Reasoning output tokens: %d\n", usage.ReasoningOutputTokens)
	_, _ = fmt.Fprintf(report, "- Total tokens: %d\n", usage.TotalTokens)
}
