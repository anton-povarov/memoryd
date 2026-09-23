package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/anton-povarov/memoryd/server/internal/api"

	"github.com/swaggest/swgui/v5emb"
)

// RegisterDocumentationEndpoint registers the OpenAPI and Swagger UI routes
func RegisterDocumentationEndpoint(mux *http.ServeMux) error {
	if mux == nil {
		return errors.New("apidoc: nil HTTP router")
	}

	jsonSpec, err := api.OpenAPIJSON()
	if err != nil {
		return fmt.Errorf("apidoc: load OpenAPI specification: %w", err)
	}

	jsonSpec, title, err := prepareSpec(jsonSpec)
	if err != nil {
		return err
	}

	openAPIJSONPath := "/openapi.json"
	docsPath := "/docs"
	docsBasePath := docsPath + "/"

	jsonHandler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(jsonSpec)
	}
	mux.HandleFunc(http.MethodGet+" "+"/openapi.json", jsonHandler)
	mux.HandleFunc(http.MethodGet+" "+"/.well-known"+openAPIJSONPath, jsonHandler)

	mux.HandleFunc(http.MethodGet+" "+docsPath, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, docsBasePath, http.StatusPermanentRedirect)
	})

	mux.Handle(http.MethodGet+" "+docsBasePath, v5emb.New(title, openAPIJSONPath, docsBasePath))

	return nil
}

func prepareSpec(jsonSpec []byte) ([]byte, string, error) {
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

	title := ""
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
