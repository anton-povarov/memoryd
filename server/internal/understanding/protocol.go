// Package understanding builds and executes Understanding Plugin v2 requests.
package understanding

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/anton-povarov/memoryd/server/internal/vault"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	ProtocolVersion   = 2
	MarkdownMediaType = "text/markdown"
	JSONMediaType     = "application/json"
)

// Request is the complete, path-independent input sent to one plugin process.
type Request struct {
	ProtocolVersion int                  `json:"protocol_version"`
	Blob            RequestBlob          `json:"blob"`
	ImportContext   RequestImportContext `json:"import_context"`
	Model           *RequestModel        `json:"model,omitempty"`
}

// RequestModel selects the local Codex app-server model configuration for a
// plugin. Command contains only the executable and fixed app-server arguments;
// authentication remains in the local Codex installation.
type RequestModel struct {
	Provider        string   `json:"provider" yaml:"provider"`
	Name            string   `json:"name" yaml:"name"`
	ReasoningEffort string   `json:"reasoning_effort" yaml:"reasoning_effort"`
	Command         []string `json:"command" yaml:"command"`
}

// Validate checks that a model selection is complete and cannot carry
// credentials or arbitrary arguments to the plugin.
func (model RequestModel) Validate() error {
	if model.Provider != "codex_app_server" {
		return errors.New(`model provider must be "codex_app_server"`)
	}
	if strings.TrimSpace(model.Name) == "" {
		return errors.New("model name must not be empty")
	}
	switch model.ReasoningEffort {
	case "minimal", "low", "medium", "high", "xhigh":
	default:
		return errors.New("model reasoning_effort must be minimal, low, medium, high, or xhigh")
	}
	if len(model.Command) != 3 || strings.TrimSpace(model.Command[0]) == "" ||
		model.Command[1] != "app-server" || model.Command[2] != "--stdio" {
		return errors.New(
			"model command must contain a nonempty executable followed by app-server --stdio",
		)
	}
	return nil
}

type RequestBlob struct {
	Blobref       string `json:"blobref"`
	MediaType     string `json:"media_type"`
	ByteSize      int64  `json:"byte_size"`
	ContentBase64 string `json:"content_base64"`
}

type RequestImportContext struct {
	OriginalFilename   string     `json:"original_filename"`
	RelativePath       string     `json:"relative_path,omitempty"`
	FullPath           string     `json:"full_path,omitempty"`
	FilesystemCreated  *time.Time `json:"filesystem_created_at,omitempty"`
	FilesystemModified *time.Time `json:"filesystem_modified_at,omitempty"`
}

// BuildRequest applies the Vault import-context normalization, derives the
// canonical Blobref and serializes the exact request bytes for the plugin.
func BuildRequest(
	content []byte,
	mediaType string,
	importContext vault.ImportContext,
	models ...RequestModel,
) (Request, []byte, error) {
	if len(models) > 1 {
		return Request{}, nil, errors.New("at most one model configuration may be supplied")
	}
	var model *RequestModel
	if len(models) == 1 {
		if err := models[0].Validate(); err != nil {
			return Request{}, nil, fmt.Errorf("invalid model configuration: %w", err)
		}
		model = &models[0]
	}
	normalizedContext, err := importContext.Normalized()
	if err != nil {
		return Request{}, nil, err
	}
	parsedMediaType, _, err := mime.ParseMediaType(mediaType)
	if err != nil || !strings.Contains(parsedMediaType, "/") ||
		strings.Contains(parsedMediaType, "*") {
		if err != nil {
			return Request{}, nil, fmt.Errorf("invalid Blob Media Type %q: %w", mediaType, err)
		}
		return Request{}, nil, fmt.Errorf(
			"invalid Blob Media Type %q: expected type/subtype",
			mediaType,
		)
	}

	ref := vault.NewSHA256Blobref(sha256.Sum256(content))
	request := Request{
		ProtocolVersion: ProtocolVersion,
		Blob: RequestBlob{
			Blobref:       ref.String(),
			MediaType:     parsedMediaType,
			ByteSize:      int64(len(content)),
			ContentBase64: base64.StdEncoding.EncodeToString(content),
		},
		ImportContext: RequestImportContext{
			OriginalFilename:   normalizedContext.OriginalFilename,
			RelativePath:       normalizedContext.RelativePath,
			FullPath:           normalizedContext.FullPath,
			FilesystemCreated:  normalizedContext.FilesystemCreated,
			FilesystemModified: normalizedContext.FilesystemModified,
		},
		Model: model,
	}
	requestBytes, err := json.Marshal(request)
	if err != nil {
		return Request{}, nil, fmt.Errorf("encode plugin request: %w", err)
	}
	return request, requestBytes, nil
}

// Artifact is one independently attributed text output.
type Artifact struct {
	ContentType string
	Content     string
	Provenance  json.RawMessage
	Scope       json.RawMessage
}

// Result is one complete protocol v2 response with optional Run reporting.
type Result struct {
	ProtocolVersion int
	PluginVersion   string
	Artifacts       []Artifact
	Warnings        []string
	Statistics      *vault.Statistics
	CostEstimate    *vault.CostEstimate
}

//go:embed plugin-response-v2.schema.json
var responseSchemaJSON []byte

var responseSchema = compileResponseSchema()

func compileResponseSchema() *jsonschema.Schema {
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(responseSchemaJSON))
	if err != nil {
		panic(err)
	}
	compiler := jsonschema.NewCompiler()
	const location = "plugin-response-v2.schema.json"
	if err := compiler.AddResource(location, document); err != nil {
		panic(err)
	}
	schema, err := compiler.Compile(location)
	if err != nil {
		panic(err)
	}
	return schema
}

// ValidateResult accepts one UTF-8 JSON object following the v2 response schema.
func ValidateResult(stdout []byte) (Result, error) {
	if len(stdout) == 0 {
		return Result{}, errors.New("plugin produced no result on stdout")
	}
	if !utf8.Valid(stdout) {
		return Result{}, errors.New("plugin stdout is not valid UTF-8")
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(stdout))
	if err != nil {
		return Result{}, fmt.Errorf("decode plugin result JSON: %w", err)
	}
	if err := responseSchema.Validate(document); err != nil {
		return Result{}, fmt.Errorf("invalid plugin result: %w", err)
	}
	// Core types are guaranteed by the schema above.
	fields, _ := document.(map[string]any)
	artifactValues, _ := fields["artifacts"].([]any)
	result := Result{
		ProtocolVersion: ProtocolVersion,
		Artifacts:       make([]Artifact, 0, len(artifactValues)),
	}
	for _, value := range artifactValues {
		artifact, _ := value.(map[string]any)
		contentType, _ := artifact["content_type"].(string)
		content, _ := artifact["content"].(string)
		result.Artifacts = append(result.Artifacts, Artifact{
			ContentType: contentType,
			Content:     content,
			Provenance:  optionalJSON(artifact, "provenance"),
			Scope:       optionalJSON(artifact, "scope"),
		})
	}
	for name, value := range fields {
		switch name {
		case "protocol_version", "artifacts":
			continue
		case "plugin_version":
			if version, ok := value.(string); ok {
				result.PluginVersion = version
				continue
			}
		case "warnings":
			if values, ok := value.([]any); ok {
				warnings := make([]string, 0, len(values))
				for _, value := range values {
					warning, ok := value.(string)
					if !ok {
						break
					}
					warnings = append(warnings, warning)
				}
				if len(warnings) == len(values) {
					result.Warnings = warnings
					continue
				}
			}
		}
	}
	var reporting struct {
		Statistics   *vault.Statistics   `json:"statistics"`
		CostEstimate *vault.CostEstimate `json:"cost_estimate"`
	}
	if err := json.Unmarshal(stdout, &reporting); err != nil {
		return Result{}, fmt.Errorf("decode plugin reporting: %w", err)
	}
	result.Statistics = reporting.Statistics
	result.CostEstimate = reporting.CostEstimate
	return result, nil
}

func optionalJSON(fields map[string]any, name string) json.RawMessage {
	value, exists := fields[name]
	if !exists {
		return nil
	}
	// The decoded tree contains only JSON values, so serialization cannot fail.
	raw, _ := json.Marshal(value)
	return raw
}
