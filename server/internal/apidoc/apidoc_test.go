package apidoc

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

const testSpec = `openapi: 3.0.3
info:
  title: Memory Vault API
  version: 0.1.0
paths:
  /memories:
    get:
      responses:
        '200':
          description: OK
`

func TestRegisterDocumentationEndpoint(t *testing.T) {
	e := echo.New()
	if err := RegisterDocumentationEndpoint(e, "/api/", []byte(testSpec)); err != nil {
		t.Fatalf("register documentation: %v", err)
	}

	tests := []struct {
		name        string
		method      string
		path        string
		status      int
		content     string
		mustContain string
	}{
		{name: "yaml", method: http.MethodGet, path: "/api/openapi.yaml", status: http.StatusOK, content: "text/yaml", mustContain: "openapi: 3.0.3"},
		{name: "json", method: http.MethodGet, path: "/api/openapi.json", status: http.StatusOK, content: "application/json", mustContain: `"Memory Vault API"`},
		{name: "docs redirect", method: http.MethodGet, path: "/api/docs", status: http.StatusPermanentRedirect, content: "", mustContain: ""},
		{name: "docs UI", method: http.MethodGet, path: "/api/docs/", status: http.StatusOK, content: "text/html", mustContain: "swagger-ui"},
		{name: "embedded CSS", method: http.MethodGet, path: "/api/docs/swagger-ui.css", status: http.StatusOK, content: "text/css", mustContain: ".swagger-ui"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			res := httptest.NewRecorder()
			e.ServeHTTP(res, req)

			if res.Code != tc.status {
				t.Fatalf("status = %d, want %d (body: %s)", res.Code, tc.status, res.Body.String())
			}
			if tc.content != "" && !strings.Contains(res.Header().Get("Content-Type"), tc.content) {
				t.Fatalf("Content-Type = %q, want %q", res.Header().Get("Content-Type"), tc.content)
			}
			if tc.mustContain != "" && !strings.Contains(res.Body.String(), tc.mustContain) {
				t.Fatalf("body does not contain %q", tc.mustContain)
			}
			if tc.name == "docs redirect" && res.Header().Get("Location") != "/api/docs/" {
				t.Fatalf("Location = %q, want /api/docs/", res.Header().Get("Location"))
			}
		})
	}
}

func TestOpenAPISpecIsJSONAndInputIsCopied(t *testing.T) {
	e := echo.New()
	spec := []byte(testSpec)
	if err := RegisterDocumentationEndpoint(e, "", spec); err != nil {
		t.Fatalf("register documentation: %v", err)
	}
	for i := range spec {
		spec[i] = 'x'
	}

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	res := httptest.NewRecorder()
	e.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	var document map[string]any
	if err := json.NewDecoder(res.Body).Decode(&document); err != nil {
		t.Fatalf("decode OpenAPI JSON: %v", err)
	}
	if got := document["openapi"]; got != "3.0.3" {
		t.Fatalf("openapi = %#v, want 3.0.3", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil)
	res = httptest.NewRecorder()
	e.ServeHTTP(res, req)
	body, _ := io.ReadAll(res.Body)
	if string(body) != testSpec {
		t.Fatalf("YAML response changed after caller mutation")
	}
}

func TestRegisterDocumentationEndpointRejectsInvalidInput(t *testing.T) {
	if err := RegisterDocumentationEndpoint(echo.New(), "/api", []byte("openapi: [")); err == nil {
		t.Fatal("invalid YAML unexpectedly accepted")
	}
	if err := RegisterDocumentationEndpoint(nil, "/api", []byte(testSpec)); err == nil {
		t.Fatal("nil router unexpectedly accepted")
	}
	if err := RegisterDocumentationEndpoint(echo.New(), "api", []byte(testSpec)); err == nil {
		t.Fatal("relative prefix unexpectedly accepted")
	}
}
