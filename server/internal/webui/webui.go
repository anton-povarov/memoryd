// Package webui serves memoryd's embedded browser interface.
package webui

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"

	"github.com/labstack/echo/v4"
)

//go:embed assets/*
var assets embed.FS

// Register mounts the browser interface and its static assets on e.
func Register(e *echo.Echo) error {
	if e == nil {
		return fmt.Errorf("webui: nil Echo router")
	}

	assetFS, err := fs.Sub(assets, "assets")
	if err != nil {
		return fmt.Errorf("webui: open embedded assets: %w", err)
	}

	e.GET("/", func(c echo.Context) error {
		return serveAsset(c, assetFS, "index.html", "text/html; charset=utf-8")
	})
	e.GET("/assets/app.css", func(c echo.Context) error {
		return serveAsset(c, assetFS, "app.css", "text/css; charset=utf-8")
	})
	e.GET("/assets/app.js", func(c echo.Context) error {
		return serveAsset(c, assetFS, "app.js", "text/javascript; charset=utf-8")
	})

	return nil
}

func serveAsset(
	c echo.Context,
	assetFS fs.FS,
	name string,
	contentType string,
) error {
	content, err := fs.ReadFile(assetFS, name)
	if err != nil {
		return fmt.Errorf("webui: read %s: %w", name, err)
	}

	c.Response().Header().Set("Cache-Control", "no-cache")
	return c.Blob(http.StatusOK, contentType, content)
}
