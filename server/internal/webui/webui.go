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

	indexHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, assetFS, "index.html")
	})
	mux.Handle(http.MethodGet+" /{$}", noCache(indexHandler))
	assetHandler := http.StripPrefix("/assets/", http.FileServerFS(assetFS))
	mux.Handle(http.MethodGet+" /assets/", noCache(assetHandler))

	return nil
}

func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}
