package webui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anton-povarov/memoryd/server/internal/webui"
	"github.com/labstack/echo/v4"
)

func TestRegisterServesEmbeddedInterface(t *testing.T) {
	e := echo.New()
	if err := webui.Register(e); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		path        string
		contentType string
		contains    string
	}{
		{
			path:        "/",
			contentType: "text/html",
			contains:    "Your memory, kept close",
		},
		{
			path:        "/assets/app.css",
			contentType: "text/css",
			contains:    "--color-ink",
		},
		{
			path:        "/assets/app.js",
			contentType: "text/javascript",
			contains:    `const apiBase = "/api/v0"`,
		},
	} {
		recorder := httptest.NewRecorder()
		e.ServeHTTP(
			recorder,
			httptest.NewRequest(http.MethodGet, test.path, nil),
		)
		if recorder.Code != http.StatusOK {
			t.Errorf("GET %s status = %d", test.path, recorder.Code)
		}
		if !strings.Contains(
			recorder.Header().Get("Content-Type"),
			test.contentType,
		) {
			t.Errorf(
				"GET %s Content-Type = %q",
				test.path,
				recorder.Header().Get("Content-Type"),
			)
		}
		if !strings.Contains(recorder.Body.String(), test.contains) {
			t.Errorf("GET %s response is missing %q", test.path, test.contains)
		}
	}
}

func TestRegisterRejectsNilRouter(t *testing.T) {
	if err := webui.Register(nil); err == nil {
		t.Fatal("Register(nil) returned nil error")
	}
}

func TestInterfaceSupportsSelectingMultipleFiles(t *testing.T) {
	e := echo.New()
	if err := webui.Register(e); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	e.ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/", nil),
	)
	for _, expected := range []string{
		`id="file-input" type="file" name="file" multiple`,
		`id="file-queue"`,
		"Choose files or drop them here",
	} {
		if !strings.Contains(recorder.Body.String(), expected) {
			t.Errorf("interface is missing %q", expected)
		}
	}
}
