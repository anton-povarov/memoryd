// Package understanding builds and executes Understanding Plugin v1 requests.
package understanding

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"mime"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/anton-povarov/memoryd/server/internal/vault"
)

const (
	ProtocolVersion   = 1
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
	Provider        string   `json:"provider"`
	Name            string   `json:"name"`
	ReasoningEffort string   `json:"reasoning_effort"`
	Command         []string `json:"command"`
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
	if len(model.Command) != 3 || !filepath.IsAbs(model.Command[0]) ||
		model.Command[1] != "app-server" || model.Command[2] != "--stdio" {
		return errors.New(
			"model command must contain an absolute executable followed by app-server --stdio",
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

// Artifact is one independently attributed plugin output.
type Artifact struct {
	Kind       string
	MediaType  string
	Content    string
	Provenance map[string]json.RawMessage
	Scope      map[string]json.RawMessage
}

// Result is one complete protocol v1 response.
type Result struct {
	ProtocolVersion int
	PluginVersion   string
	Artifacts       []Artifact
	Warnings        []string
	Usage           *Usage
	TurnUsage       []TurnUsageEntry
	CostEstimate    *CostEstimate
}

// TurnUsageEntry contains token counts for one model turn reported by a plugin.
type TurnUsageEntry struct {
	Turn  uint64 `json:"turn"`
	Usage Usage  `json:"usage"`
}

// Usage contains token counts reported by a plugin. CacheWriteInputTokens is
// optional because not every provider reports cache writes.
type Usage struct {
	InputTokens           uint64  `json:"input_tokens"`
	CachedInputTokens     uint64  `json:"cached_input_tokens"`
	CacheWriteInputTokens *uint64 `json:"cache_write_input_tokens,omitempty"`
	OutputTokens          uint64  `json:"output_tokens"`
	ReasoningOutputTokens uint64  `json:"reasoning_output_tokens"`
	TotalTokens           uint64  `json:"total_tokens"`
}

// CostEstimate is an estimate supplied by the plugin using its own pricing
// source and basis. It is not a measured subscription charge.
type CostEstimate struct {
	AmountUSD   float64 `json:"amount_usd"`
	Basis       string  `json:"basis"`
	PricingDate string  `json:"pricing_date"`
	PricingURL  string  `json:"pricing_url"`
}

// ValidateResult accepts one UTF-8 JSON object that follows the v1 result
// shape. Unknown fields are retained by neither the runner nor report.
func ValidateResult(stdout []byte) (Result, error) {
	if len(stdout) == 0 {
		return Result{}, errors.New("plugin produced no result on stdout")
	}
	if !utf8.Valid(stdout) {
		return Result{}, errors.New("plugin stdout is not valid UTF-8")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(stdout, &fields); err != nil {
		return Result{}, fmt.Errorf("decode plugin result JSON: %w", err)
	}
	if fields == nil {
		return Result{}, errors.New("plugin result must be a JSON object")
	}

	var result Result
	protocolVersion, ok := fields["protocol_version"]
	if !ok {
		return Result{}, errors.New("plugin result is missing protocol_version")
	}
	if err := json.Unmarshal(protocolVersion, &result.ProtocolVersion); err != nil {
		return Result{}, fmt.Errorf("plugin result protocol_version must be an integer: %w", err)
	}
	if result.ProtocolVersion != ProtocolVersion {
		return Result{}, fmt.Errorf(
			"unsupported plugin protocol version %d",
			result.ProtocolVersion,
		)
	}

	pluginVersion, ok := fields["plugin_version"]
	if !ok {
		return Result{}, errors.New("plugin result is missing plugin_version")
	}
	if err := decodeString(pluginVersion, &result.PluginVersion); err != nil {
		return Result{}, fmt.Errorf("plugin result plugin_version must be a string: %w", err)
	}
	if strings.TrimSpace(result.PluginVersion) == "" {
		return Result{}, errors.New("plugin result plugin_version must not be empty")
	}

	artifactsJSON, ok := fields["artifacts"]
	if !ok || !jsonBeginsWith(artifactsJSON, '[') {
		return Result{}, errors.New("plugin result artifacts must be an array")
	}
	var artifactFields []json.RawMessage
	if err := json.Unmarshal(artifactsJSON, &artifactFields); err != nil {
		return Result{}, fmt.Errorf("decode plugin artifacts: %w", err)
	}
	result.Artifacts = make([]Artifact, 0, len(artifactFields))
	for index, artifactJSON := range artifactFields {
		artifact, err := decodeArtifact(artifactJSON)
		if err != nil {
			return Result{}, fmt.Errorf("plugin artifact %d: %w", index+1, err)
		}
		result.Artifacts = append(result.Artifacts, artifact)
	}

	warningsJSON, exists := fields["warnings"]
	if !exists || !jsonBeginsWith(warningsJSON, '[') {
		return Result{}, errors.New("plugin result warnings must be an array of strings")
	}
	if err := json.Unmarshal(warningsJSON, &result.Warnings); err != nil {
		return Result{}, fmt.Errorf("decode plugin warnings: %w", err)
	}
	usage, turnUsage, err := decodeReportedUsage(fields)
	if err != nil {
		return Result{}, err
	}
	result.Usage = usage
	result.TurnUsage = turnUsage
	if costJSON, exists := fields["cost_estimate"]; exists {
		costEstimate, err := decodeCostEstimate(costJSON)
		if err != nil {
			return Result{}, fmt.Errorf("plugin result cost_estimate: %w", err)
		}
		result.CostEstimate = &costEstimate
	}
	return result, nil
}

func decodeReportedUsage(fields map[string]json.RawMessage) (*Usage, []TurnUsageEntry, error) {
	var cumulativeUsage *Usage
	if usageJSON, exists := fields["usage"]; exists {
		usage, err := decodeUsage(usageJSON)
		if err != nil {
			return nil, nil, fmt.Errorf("plugin result usage: %w", err)
		}
		cumulativeUsage = &usage
	}
	var turnUsage []TurnUsageEntry
	if turnUsageJSON, exists := fields["turn_usage"]; exists {
		if !jsonBeginsWith(turnUsageJSON, '[') {
			return nil, nil, errors.New("plugin result turn_usage must be an array")
		}
		var entries []json.RawMessage
		if err := json.Unmarshal(turnUsageJSON, &entries); err != nil {
			return nil, nil, fmt.Errorf("decode plugin result turn_usage: %w", err)
		}
		turnUsage = make([]TurnUsageEntry, 0, len(entries))
		seenTurns := make(map[uint64]struct{}, len(entries))
		for index, entryJSON := range entries {
			entry, err := decodeTurnUsageEntry(entryJSON)
			if err != nil {
				return nil, nil, fmt.Errorf("plugin result turn_usage entry %d: %w", index+1, err)
			}
			if _, exists := seenTurns[entry.Turn]; exists {
				return nil, nil, fmt.Errorf(
					"plugin result turn_usage entry %d: duplicate turn %d",
					index+1,
					entry.Turn,
				)
			}
			seenTurns[entry.Turn] = struct{}{}
			turnUsage = append(turnUsage, entry)
		}
		if cumulativeUsage != nil && len(turnUsage) > 0 {
			if err := validateTurnUsageTotal(*cumulativeUsage, turnUsage); err != nil {
				return nil, nil, fmt.Errorf("plugin result turn_usage: %w", err)
			}
		}
	}
	return cumulativeUsage, turnUsage, nil
}

func decodeTurnUsageEntry(raw json.RawMessage) (TurnUsageEntry, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		if err != nil {
			return TurnUsageEntry{}, fmt.Errorf("must be a JSON object: %w", err)
		}
		return TurnUsageEntry{}, errors.New("must be a JSON object")
	}
	turnJSON, exists := fields["turn"]
	if !exists {
		return TurnUsageEntry{}, errors.New("turn is required")
	}
	var entry TurnUsageEntry
	if err := decodeTokenCount(turnJSON, &entry.Turn); err != nil {
		return TurnUsageEntry{}, fmt.Errorf("turn must be a positive integer: %w", err)
	}
	if entry.Turn == 0 {
		return TurnUsageEntry{}, errors.New("turn must be a positive integer")
	}
	usageJSON, exists := fields["usage"]
	if !exists {
		return TurnUsageEntry{}, errors.New("usage is required")
	}
	usage, err := decodeUsage(usageJSON)
	if err != nil {
		return TurnUsageEntry{}, fmt.Errorf("usage: %w", err)
	}
	entry.Usage = usage
	return entry, nil
}

func validateTurnUsageTotal(total Usage, turns []TurnUsageEntry) error {
	checks := []struct {
		name string
		want uint64
		read func(Usage) uint64
	}{
		{
			name: "input_tokens",
			want: total.InputTokens,
			read: func(usage Usage) uint64 { return usage.InputTokens },
		},
		{
			name: "cached_input_tokens",
			want: total.CachedInputTokens,
			read: func(usage Usage) uint64 { return usage.CachedInputTokens },
		},
		{
			name: "output_tokens",
			want: total.OutputTokens,
			read: func(usage Usage) uint64 { return usage.OutputTokens },
		},
		{
			name: "reasoning_output_tokens",
			want: total.ReasoningOutputTokens,
			read: func(usage Usage) uint64 { return usage.ReasoningOutputTokens },
		},
		{
			name: "total_tokens",
			want: total.TotalTokens,
			read: func(usage Usage) uint64 { return usage.TotalTokens },
		},
	}
	for _, check := range checks {
		sum, err := sumTurnUsage(turns, check.name, check.read)
		if err != nil {
			return err
		}
		if sum != check.want {
			return fmt.Errorf("sum of %s is %d, but usage reports %d", check.name, sum, check.want)
		}
	}
	if total.CacheWriteInputTokens != nil {
		for _, turn := range turns {
			if turn.Usage.CacheWriteInputTokens == nil {
				return errors.New(
					"each turn must report cache_write_input_tokens when usage reports it",
				)
			}
		}
		sum, err := sumTurnUsage(turns, "cache_write_input_tokens", func(usage Usage) uint64 {
			return *usage.CacheWriteInputTokens
		})
		if err != nil {
			return err
		}
		if sum != *total.CacheWriteInputTokens {
			return fmt.Errorf(
				"sum of cache_write_input_tokens is %d, but usage reports %d",
				sum,
				*total.CacheWriteInputTokens,
			)
		}
	}
	return nil
}

func sumTurnUsage(
	turns []TurnUsageEntry,
	field string,
	read func(Usage) uint64,
) (uint64, error) {
	var sum uint64
	for _, turn := range turns {
		count := read(turn.Usage)
		if ^uint64(0)-sum < count {
			return 0, fmt.Errorf("sum of %s overflows an unsigned integer", field)
		}
		sum += count
	}
	return sum, nil
}

func decodeUsage(raw json.RawMessage) (Usage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		if err != nil {
			return Usage{}, fmt.Errorf("must be a JSON object: %w", err)
		}
		return Usage{}, errors.New("must be a JSON object")
	}
	var usage Usage
	for name, destination := range map[string]*uint64{
		"input_tokens":            &usage.InputTokens,
		"cached_input_tokens":     &usage.CachedInputTokens,
		"output_tokens":           &usage.OutputTokens,
		"reasoning_output_tokens": &usage.ReasoningOutputTokens,
		"total_tokens":            &usage.TotalTokens,
	} {
		value, exists := fields[name]
		if !exists {
			return Usage{}, fmt.Errorf("%s is required", name)
		}
		if err := decodeTokenCount(value, destination); err != nil {
			return Usage{}, fmt.Errorf("%s must be a nonnegative integer: %w", name, err)
		}
	}
	if value, exists := fields["cache_write_input_tokens"]; exists {
		var count uint64
		if err := decodeTokenCount(value, &count); err != nil {
			return Usage{}, fmt.Errorf(
				"cache_write_input_tokens must be a nonnegative integer: %w",
				err,
			)
		}
		usage.CacheWriteInputTokens = &count
	}
	return usage, nil
}

func decodeTokenCount(raw json.RawMessage, destination *uint64) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] < '0' || trimmed[0] > '9' {
		return errors.New("value is not a nonnegative integer")
	}
	return json.Unmarshal(raw, destination)
}

func decodeCostEstimate(raw json.RawMessage) (CostEstimate, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		if err != nil {
			return CostEstimate{}, fmt.Errorf("must be a JSON object: %w", err)
		}
		return CostEstimate{}, errors.New("must be a JSON object")
	}
	var estimate CostEstimate
	amount, exists := fields["amount_usd"]
	if !exists {
		return CostEstimate{}, errors.New("amount_usd is required")
	}
	trimmedAmount := bytes.TrimSpace(amount)
	if len(trimmedAmount) == 0 ||
		(trimmedAmount[0] != '-' && (trimmedAmount[0] < '0' || trimmedAmount[0] > '9')) {
		return CostEstimate{}, errors.New("amount_usd must be a nonnegative number")
	}
	if err := json.Unmarshal(amount, &estimate.AmountUSD); err != nil {
		return CostEstimate{}, fmt.Errorf("amount_usd must be a nonnegative number: %w", err)
	}
	if math.IsNaN(estimate.AmountUSD) || math.IsInf(estimate.AmountUSD, 0) ||
		estimate.AmountUSD < 0 {
		return CostEstimate{}, errors.New("amount_usd must be a nonnegative number")
	}
	for name, destination := range map[string]*string{
		"basis":        &estimate.Basis,
		"pricing_date": &estimate.PricingDate,
		"pricing_url":  &estimate.PricingURL,
	} {
		value, exists := fields[name]
		if !exists {
			return CostEstimate{}, fmt.Errorf("%s is required", name)
		}
		if err := decodeString(value, destination); err != nil {
			return CostEstimate{}, fmt.Errorf("%s must be a string: %w", name, err)
		}
		if strings.TrimSpace(*destination) == "" {
			return CostEstimate{}, fmt.Errorf("%s must not be empty", name)
		}
	}
	parsedDate, err := time.Parse("2006-01-02", estimate.PricingDate)
	if err != nil || parsedDate.Format("2006-01-02") != estimate.PricingDate {
		return CostEstimate{}, errors.New("pricing_date must be a valid YYYY-MM-DD date")
	}
	return estimate, nil
}

func decodeArtifact(raw json.RawMessage) (Artifact, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		if err != nil {
			return Artifact{}, fmt.Errorf("must be a JSON object: %w", err)
		}
		return Artifact{}, errors.New("must be a JSON object")
	}
	var artifact Artifact
	for name, destination := range map[string]*string{
		"kind":       &artifact.Kind,
		"media_type": &artifact.MediaType,
		"content":    &artifact.Content,
	} {
		value, exists := fields[name]
		if !exists || !jsonBeginsWith(value, '"') {
			return Artifact{}, fmt.Errorf("%s must be a string", name)
		}
		if err := decodeString(value, destination); err != nil {
			return Artifact{}, fmt.Errorf("%s must be a string: %w", name, err)
		}
	}
	if strings.TrimSpace(artifact.Kind) == "" {
		return Artifact{}, errors.New("kind must not be empty")
	}
	switch artifact.MediaType {
	case MarkdownMediaType:
	case JSONMediaType:
		if err := validateJSONObject([]byte(artifact.Content)); err != nil {
			return Artifact{}, fmt.Errorf("application/json content must be a JSON object: %w", err)
		}
	default:
		return Artifact{}, fmt.Errorf(
			"media_type must be %q or %q",
			MarkdownMediaType,
			JSONMediaType,
		)
	}
	provenanceJSON, exists := fields["provenance"]
	if !exists || !jsonBeginsWith(provenanceJSON, '{') {
		return Artifact{}, errors.New("provenance must be a JSON object")
	}
	if err := json.Unmarshal(provenanceJSON, &artifact.Provenance); err != nil {
		return Artifact{}, fmt.Errorf("decode provenance: %w", err)
	}
	if scopeJSON, exists := fields["scope"]; exists {
		if !jsonBeginsWith(scopeJSON, '{') {
			return Artifact{}, errors.New("scope must be a JSON object")
		}
		if err := json.Unmarshal(scopeJSON, &artifact.Scope); err != nil {
			return Artifact{}, fmt.Errorf("decode scope: %w", err)
		}
	}
	return artifact, nil
}

func validateJSONObject(content []byte) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(content, &object); err != nil {
		return err
	}
	if object == nil {
		return errors.New("top-level value must be an object")
	}
	return nil
}

func decodeString(raw json.RawMessage, destination *string) error {
	if !jsonBeginsWith(raw, '"') {
		return errors.New("value is not a JSON string")
	}
	return json.Unmarshal(raw, destination)
}

func jsonBeginsWith(raw json.RawMessage, prefix byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == prefix
}
