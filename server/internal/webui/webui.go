// Package webui serves memoryd's embedded browser interface.
package webui

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
)

//go:embed assets/*
var assets embed.FS

// Register mounts the browser interface and its static assets on mux.
func Register(mux *http.ServeMux) error {
	if mux == nil {
		return fmt.Errorf("webui: nil HTTP router")
	}

	assetFS, err := fs.Sub(assets, "assets")
	if err != nil {
		return fmt.Errorf("webui: open embedded assets: %w", err)
	}

	mux.Handle(http.MethodGet+" /", http.FileServerFS(assetFS))

	return nil
}
