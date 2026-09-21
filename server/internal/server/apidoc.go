package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/swaggest/swgui/v5emb"
	"sigs.k8s.io/yaml"
)

// RegisterDocumentationEndpoint registers the OpenAPI and Swagger UI routes
// below urlPrefix. A prefix of "", "/", or "/api/" is accepted; the routes
// are normalized so that the resulting paths are, for example,
// /docs, /docs/, /openapi.yaml, and /openapi.json (or the same paths below
// /api).
//
// The specification is validated and converted to JSON when the routes are
// registered. The input bytes are copied, so callers may safely reuse or
// mutate their input after this function returns.
func RegisterDocumentationEndpoint(
	mux *http.ServeMux,
	urlPrefix string,
	yamlSpec []byte,
) error {
	if mux == nil {
		return errors.New("apidoc: nil HTTP router")
	}

	prefix, err := normalizePrefix(urlPrefix)
	if err != nil {
		return err
	}

	yamlCopy := append([]byte(nil), yamlSpec...)
	jsonSpec, title, err := prepareSpec(yamlCopy, prefix)
	if err != nil {
		return err
	}

	openAPIYAMLPath := prefix + "/openapi.yaml"
	openAPIYMLPath := prefix + "/openapi.yml"
	openAPIJSONPath := prefix + "/openapi.json"
	docsPath := prefix + "/docs"
	docsBasePath := docsPath + "/"

	yamlHandler := func(w http.ResponseWriter, _ *http.Request) {
		// text/yaml is intentionally used here: browsers display this response
		// instead of treating an unknown application type as a download.
		w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(yamlCopy)
	}
	mux.HandleFunc(http.MethodGet+" "+openAPIYAMLPath, yamlHandler)
	// Keep the conventional .yml spelling as a harmless compatibility alias.
	mux.HandleFunc(http.MethodGet+" "+openAPIYMLPath, yamlHandler)
	mux.HandleFunc(
		http.MethodGet+" "+openAPIJSONPath,
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(jsonSpec)
		},
	)
	mux.HandleFunc(http.MethodGet+" "+docsPath, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, docsBasePath, http.StatusPermanentRedirect)
	})

	// v5emb serves both the HTML shell and its static JS/CSS from embedded
	// assets. It does not require the browser to reach a CDN.
	ui := v5emb.New(title, openAPIJSONPath, docsBasePath)
	mux.Handle(http.MethodGet+" "+docsBasePath, ui)

	return nil
}

// normalizePrefix keeps route construction in one place and rejects values
// that could accidentally introduce a query, fragment, or path parameters.
func normalizePrefix(prefix string) (string, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || prefix == "/" {
		return "", nil
	}
	if !strings.HasPrefix(prefix, "/") {
		return "", fmt.Errorf(
			"apidoc: URL prefix %q must start with '/'",
			prefix,
		)
	}
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" {
		return "", nil
	}
	if strings.ContainsAny(prefix, "?#") {
		return "", fmt.Errorf(
			"apidoc: URL prefix %q must not contain a query or fragment",
			prefix,
		)
	}
	for _, segment := range strings.Split(strings.TrimPrefix(prefix, "/"), "/") {
		if segment == "." || segment == ".." ||
			strings.ContainsAny(segment, "{}") {
			return "", fmt.Errorf(
				"apidoc: URL prefix %q contains an invalid path segment",
				prefix,
			)
		}
	}
	return prefix, nil
}

func prepareSpec(spec []byte, prefix string) ([]byte, string, error) {
	jsonSpec, err := yaml.YAMLToJSON(spec)
	if err != nil {
		return nil, "", fmt.Errorf("apidoc: invalid OpenAPI YAML: %w", err)
	}

	var document map[string]any
	if err := json.Unmarshal(jsonSpec, &document); err != nil {
		return nil, "", fmt.Errorf(
			"apidoc: OpenAPI document must be an object: %w",
			err,
		)
	}
	if document == nil {
		return nil, "", errors.New("apidoc: OpenAPI document must be an object")
	}

	title := prefix
	if info, ok := document["info"].(map[string]any); ok {
		if value, ok := info["title"].(string); ok &&
			strings.TrimSpace(value) != "" {
			title = value
		}
	}
	if title == "" {
		title = "OpenAPI documentation"
	}

	// Compact JSON is deterministic and avoids injecting caller-controlled YAML
	// into the UI page as text.
	var compact bytes.Buffer
	if err := json.Compact(&compact, jsonSpec); err != nil {
		return nil, "", fmt.Errorf(
			"apidoc: invalid converted OpenAPI JSON: %w",
			err,
		)
	}
	return compact.Bytes(), title, nil
}
