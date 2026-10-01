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
			want: "app-server --stdio",
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

func TestValidateResultSupportsJSONDocumentData(t *testing.T) {
	content := "{\n  \"title\": \"Statement\",\n  \"pages\": [1, 2]\n}"
	result, err := ValidateResult(jsonArtifactResult(content))
	if err != nil {
		t.Fatalf("ValidateResult JSON content: %v", err)
	}
	if len(result.Artifacts) != 1 {
		t.Fatalf("artifact count = %d, want 1", len(result.Artifacts))
	}
	artifact := result.Artifacts[0]
	if artifact.ContentType != JSONMediaType || artifact.Content != content {
		t.Fatalf("decoded JSON artifact = %#v", artifact)
	}
}

func TestValidateResultRetainsObjectArtifactScope(t *testing.T) {
	resultBytes := []byte(
		`{"protocol_version":2,"plugin_version":"test-1","artifacts":[{"content_type":"text/markdown","content":"page text","provenance":{},"scope":{"page":2,"region":[1,2,3,4]}}],"warnings":[]}`,
	)
	result, err := ValidateResult(resultBytes)
	if err != nil {
		t.Fatal(err)
	}
	var scope map[string]json.RawMessage
	if err := json.Unmarshal(result.Artifacts[0].Scope, &scope); err != nil {
		t.Fatal(err)
	}
	var page int
	if err := json.Unmarshal(scope["page"], &page); err != nil || page != 2 {
		t.Fatalf("scope page = %q, decode error = %v", scope["page"], err)
	}
	if string(scope["region"]) != "[1,2,3,4]" {
		t.Fatalf("scope region = %s", scope["region"])
	}
}

func jsonArtifactResult(content string) []byte {
	result := map[string]any{
		"protocol_version": 2,
		"plugin_version":   "test-1",
		"artifacts": []any{map[string]any{
			"content_type": JSONMediaType,
			"content":      content,
			"provenance":   map[string]any{"method": "test"},
		}},
		"warnings": []string{},
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		panic(err)
	}
	return encoded
}
