package understanding

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/anton-povarov/memoryd/server/internal/vault"
)

func TestBuildRequestOmitsModelUnlessConfigured(t *testing.T) {
	_, requestBytes, err := BuildRequest(
		[]byte("hello"),
		"text/plain",
		vault.ImportContext{OriginalFilename: "note.txt"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(requestBytes), `"model"`) {
		t.Fatalf("request without model includes model: %s", requestBytes)
	}
}

func TestBuildRequestIncludesValidatedModel(t *testing.T) {
	model := RequestModel{
		Provider:        "codex_app_server",
		Name:            "gpt-6-luna",
		ReasoningEffort: "medium",
		Command:         []string{"/usr/local/bin/codex", "app-server", "--stdio"},
	}
	request, requestBytes, err := BuildRequest(
		[]byte("hello"),
		"text/plain",
		vault.ImportContext{OriginalFilename: "note.txt"},
		model,
	)
	if err != nil {
		t.Fatal(err)
	}
	if request.Model == nil {
		t.Fatal("request model is nil")
	}
	var decoded struct {
		Model RequestModel `json:"model"`
	}
	if err := json.Unmarshal(requestBytes, &decoded); err != nil {
		t.Fatalf("decode request model: %v", err)
	}
	if decoded.Model.Provider != model.Provider || decoded.Model.Name != model.Name ||
		decoded.Model.ReasoningEffort != model.ReasoningEffort ||
		len(decoded.Model.Command) != len(model.Command) {
		t.Fatalf("decoded model = %#v", decoded.Model)
	}
	for index, argument := range model.Command {
		if decoded.Model.Command[index] != argument {
			t.Fatalf("model command = %v, want %v", decoded.Model.Command, model.Command)
		}
	}
}

func TestBuildRequestRejectsIncompleteOrCredentialBearingModel(t *testing.T) {
	tests := []struct {
		name  string
		model RequestModel
		want  string
	}{
		{
			name: "provider",
			model: RequestModel{
				Provider:        "ollama",
				Name:            "gpt-6-luna",
				ReasoningEffort: "medium",
				Command:         []string{"/usr/local/bin/codex", "app-server", "--stdio"},
			},
			want: "provider",
		},
		{
			name: "missing reasoning effort",
			model: RequestModel{
				Provider: "codex_app_server",
				Name:     "gpt-6-luna",
				Command:  []string{"/usr/local/bin/codex", "app-server", "--stdio"},
			},
			want: "reasoning_effort",
		},
		{
			name: "unsupported reasoning effort",
			model: RequestModel{
				Provider:        "codex_app_server",
				Name:            "gpt-6-luna",
				ReasoningEffort: "max",
				Command:         []string{"/usr/local/bin/codex", "app-server", "--stdio"},
			},
			want: "reasoning_effort",
		},
		{
			name: "relative executable",
			model: RequestModel{
				Provider:        "codex_app_server",
				Name:            "gpt-6-luna",
				ReasoningEffort: "medium",
				Command:         []string{"codex", "app-server", "--stdio"},
			},
			want: "absolute executable",
		},
		{
			name: "extra arguments",
			model: RequestModel{
				Provider:        "codex_app_server",
				Name:            "gpt-6-luna",
				ReasoningEffort: "medium",
				Command: []string{
					"/usr/local/bin/codex",
					"app-server",
					"--stdio",
					"--api-key=secret",
				},
			},
			want: "absolute executable",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := BuildRequest(
				[]byte("hello"),
				"text/plain",
				vault.ImportContext{OriginalFilename: "note.txt"},
				test.model,
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("BuildRequest error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestValidateResultAllowsMissingUsageAndCostEstimate(t *testing.T) {
	result, err := ValidateResult(pluginResultJSON(""))
	if err != nil {
		t.Fatalf("ValidateResult without optional reporting: %v", err)
	}
	if result.Usage != nil || result.CostEstimate != nil {
		t.Fatalf(
			"optional reporting fields = usage %v, cost %v; want both absent",
			result.Usage,
			result.CostEstimate,
		)
	}
}

func TestValidateResultDecodesUsageAndCostEstimate(t *testing.T) {
	result, err := ValidateResult(pluginResultJSON(
		`,"usage":{"input_tokens":31531,"cached_input_tokens":23040,"cache_write_input_tokens":0,"output_tokens":107,"reasoning_output_tokens":0,"total_tokens":31638}` +
			`,"cost_estimate":{"amount_usd":0.001133789,"basis":"standard_api_equivalent_short_context","pricing_date":"2026-09-28","pricing_url":"https://example.com/pricing"}`,
	))
	if err != nil {
		t.Fatalf("ValidateResult with optional reporting: %v", err)
	}
	if result.Usage == nil || result.Usage.InputTokens != 31531 ||
		result.Usage.CachedInputTokens != 23040 || result.Usage.CacheWriteInputTokens == nil ||
		*result.Usage.CacheWriteInputTokens != 0 || result.Usage.OutputTokens != 107 ||
		result.Usage.ReasoningOutputTokens != 0 || result.Usage.TotalTokens != 31638 {
		t.Fatalf("decoded usage = %#v", result.Usage)
	}
	if result.CostEstimate == nil || result.CostEstimate.AmountUSD != 0.001133789 ||
		result.CostEstimate.Basis != "standard_api_equivalent_short_context" ||
		result.CostEstimate.PricingDate != "2026-09-28" ||
		result.CostEstimate.PricingURL != "https://example.com/pricing" {
		t.Fatalf("decoded cost estimate = %#v", result.CostEstimate)
	}
}

func TestValidateResultAllowsOmittedCacheWriteUsage(t *testing.T) {
	result, err := ValidateResult(pluginResultJSON(
		`,"usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0,"total_tokens":2}`,
	))
	if err != nil {
		t.Fatalf("ValidateResult without cache write count: %v", err)
	}
	if result.Usage == nil || result.Usage.CacheWriteInputTokens != nil {
		t.Fatalf("cache_write_input_tokens = %v, want absent", result.Usage)
	}
}

func TestValidateResultDecodesTurnUsageAndChecksCumulativeUsage(t *testing.T) {
	result, err := ValidateResult(pluginResultJSON(
		`,"usage":{"input_tokens":31531,"cached_input_tokens":23040,"cache_write_input_tokens":0,"output_tokens":107,"reasoning_output_tokens":0,"total_tokens":31638}` +
			`,"turn_usage":[` +
			`{"turn":2,"usage":{"input_tokens":16000,"cached_input_tokens":10040,"cache_write_input_tokens":0,"output_tokens":50,"reasoning_output_tokens":0,"total_tokens":16050}},` +
			`{"turn":1,"usage":{"input_tokens":15531,"cached_input_tokens":13000,"cache_write_input_tokens":0,"output_tokens":57,"reasoning_output_tokens":0,"total_tokens":15588}}]`,
	))
	if err != nil {
		t.Fatalf("ValidateResult with turn usage: %v", err)
	}
	if len(result.TurnUsage) != 2 || result.TurnUsage[0].Turn != 2 ||
		result.TurnUsage[0].Usage.InputTokens != 16000 || result.TurnUsage[1].Turn != 1 ||
		result.TurnUsage[1].Usage.InputTokens != 15531 {
		t.Fatalf("decoded turn usage = %#v", result.TurnUsage)
	}
}

func TestValidateResultAllowsTurnUsageWithoutCumulativeUsage(t *testing.T) {
	result, err := ValidateResult(pluginResultJSON(
		`,"turn_usage":[{"turn":1,"usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0,"total_tokens":2}}]`,
	))
	if err != nil {
		t.Fatalf("ValidateResult with turn usage only: %v", err)
	}
	if result.Usage != nil || len(result.TurnUsage) != 1 {
		t.Fatalf("usage = %v, turn usage = %#v", result.Usage, result.TurnUsage)
	}
}

func TestValidateResultRejectsMalformedOrInconsistentTurnUsage(t *testing.T) {
	validUsage := `{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0,"total_tokens":2}`
	matchingUsage := `{"input_tokens":2,"cached_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":0,"total_tokens":4}`
	tests := []struct {
		name   string
		fields string
		want   string
	}{
		{name: "array shape", fields: `,"turn_usage":null`, want: "turn_usage must be an array"},
		{
			name:   "entry object",
			fields: `,"turn_usage":[null]`,
			want:   "entry 1: must be a JSON object",
		},
		{
			name:   "required turn",
			fields: `,"turn_usage":[{"usage":` + validUsage + `}]`,
			want:   "turn is required",
		},
		{
			name:   "positive turn",
			fields: `,"turn_usage":[{"turn":0,"usage":` + validUsage + `}]`,
			want:   "turn must be a positive integer",
		},
		{
			name:   "integer turn",
			fields: `,"turn_usage":[{"turn":1.5,"usage":` + validUsage + `}]`,
			want:   "turn must be a positive integer",
		},
		{name: "required usage", fields: `,"turn_usage":[{"turn":1}]`, want: "usage is required"},
		{
			name:   "usage shape",
			fields: `,"turn_usage":[{"turn":1,"usage":null}]`,
			want:   "usage: must be a JSON object",
		},
		{
			name:   "duplicate turns",
			fields: `,"turn_usage":[{"turn":1,"usage":` + validUsage + `},{"turn":1,"usage":` + validUsage + `}]`,
			want:   "duplicate turn 1",
		},
		{
			name:   "cumulative count mismatch",
			fields: `,"usage":` + matchingUsage + `,"turn_usage":[{"turn":1,"usage":` + validUsage + `}]`,
			want:   "sum of input_tokens is 1, but usage reports 2",
		},
		{
			name:   "cache write must be reported by each turn",
			fields: `,"usage":{"input_tokens":1,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0,"total_tokens":2},"turn_usage":[{"turn":1,"usage":` + validUsage + `}]`,
			want:   "each turn must report cache_write_input_tokens",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ValidateResult(pluginResultJSON(test.fields))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateResult error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestValidateResultIgnoresPerTurnCacheWriteWhenCumulativeOmitsIt(t *testing.T) {
	result, err := ValidateResult(pluginResultJSON(
		`,"usage":{"input_tokens":2,"cached_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":0,"total_tokens":4}` +
			`,"turn_usage":[` +
			`{"turn":1,"usage":{"input_tokens":1,"cached_input_tokens":0,"cache_write_input_tokens":5,"output_tokens":1,"reasoning_output_tokens":0,"total_tokens":2}},` +
			`{"turn":2,"usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0,"total_tokens":2}}]`,
	))
	if err != nil {
		t.Fatalf("ValidateResult when aggregate omits cache-write count: %v", err)
	}
	if result.Usage == nil || result.Usage.CacheWriteInputTokens != nil {
		t.Fatalf("cumulative cache-write count = %#v, want absent", result.Usage)
	}
}

func TestValidateResultSupportsJSONDocumentData(t *testing.T) {
	content := "{\n  \"title\": \"Statement\",\n  \"pages\": [1, 2]\n}"
	result, err := ValidateResult(jsonArtifactResult(content))
	if err != nil {
		t.Fatalf("ValidateResult JSON document_data: %v", err)
	}
	if len(result.Artifacts) != 1 {
		t.Fatalf("artifact count = %d, want 1", len(result.Artifacts))
	}
	artifact := result.Artifacts[0]
	if artifact.Kind != "document_data" || artifact.MediaType != JSONMediaType ||
		artifact.Content != content {
		t.Fatalf("decoded JSON artifact = %#v", artifact)
	}
}

func TestValidateResultRejectsInvalidJSONArtifactContent(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
	}{
		{name: "malformed JSON", content: `{"title":}`},
		{name: "array", content: `[1,2]`},
		{name: "scalar", content: `"value"`},
		{name: "null", content: `null`},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ValidateResult(jsonArtifactResult(test.content))
			if err == nil ||
				!strings.Contains(err.Error(), "application/json content must be a JSON object") {
				t.Fatalf("ValidateResult error = %v, want invalid JSON object error", err)
			}
		})
	}
}

func jsonArtifactResult(content string) []byte {
	result := map[string]any{
		"protocol_version": 1,
		"plugin_version":   "test-1",
		"artifacts": []any{map[string]any{
			"kind":       "document_data",
			"media_type": JSONMediaType,
			"content":    content,
			"provenance": map[string]any{"method": "test"},
		}},
		"warnings": []string{},
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		panic(err)
	}
	return encoded
}

func TestValidateResultRejectsMalformedUsageAndCostEstimate(t *testing.T) {
	tests := []struct {
		name   string
		fields string
		want   string
	}{
		{
			name:   "usage must be object",
			fields: `,"usage":null`,
			want:   "usage: must be a JSON object",
		},
		{
			name:   "required token count",
			fields: `,"usage":{"cached_input_tokens":0,"output_tokens":0,"reasoning_output_tokens":0,"total_tokens":0}`,
			want:   "input_tokens is required",
		},
		{
			name:   "negative token count",
			fields: `,"usage":{"input_tokens":-1,"cached_input_tokens":0,"output_tokens":0,"reasoning_output_tokens":0,"total_tokens":0}`,
			want:   "input_tokens must be a nonnegative integer",
		},
		{
			name:   "fractional token count",
			fields: `,"usage":{"input_tokens":0,"cached_input_tokens":0,"output_tokens":1.5,"reasoning_output_tokens":0,"total_tokens":0}`,
			want:   "output_tokens must be a nonnegative integer",
		},
		{
			name:   "negative optional cache write count",
			fields: `,"usage":{"input_tokens":0,"cached_input_tokens":0,"cache_write_input_tokens":-1,"output_tokens":0,"reasoning_output_tokens":0,"total_tokens":0}`,
			want:   "cache_write_input_tokens must be a nonnegative integer",
		},
		{
			name:   "negative amount",
			fields: `,"cost_estimate":{"amount_usd":-0.01,"basis":"standard_api_equivalent","pricing_date":"2026-09-28","pricing_url":"https://example.com/pricing"}`,
			want:   "amount_usd must be a nonnegative number",
		},
		{
			name:   "missing cost field",
			fields: `,"cost_estimate":{"amount_usd":0.01,"pricing_date":"2026-09-28","pricing_url":"https://example.com/pricing"}`,
			want:   "basis is required",
		},
		{
			name:   "invalid pricing date",
			fields: `,"cost_estimate":{"amount_usd":0.01,"basis":"standard_api_equivalent","pricing_date":"2026-02-30","pricing_url":"https://example.com/pricing"}`,
			want:   "pricing_date must be a valid YYYY-MM-DD date",
		},
		{
			name:   "non-string pricing URL",
			fields: `,"cost_estimate":{"amount_usd":0.01,"basis":"standard_api_equivalent","pricing_date":"2026-09-28","pricing_url":null}`,
			want:   "pricing_url must be a string",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ValidateResult(pluginResultJSON(test.fields))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateResult error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func pluginResultJSON(optionalFields string) []byte {
	return []byte(
		`{"protocol_version":1,"plugin_version":"test-1","artifacts":[],"warnings":` +
			`[]` + optionalFields + `}`,
	)
}
