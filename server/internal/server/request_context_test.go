package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anton-povarov/memoryd/server/internal/logging"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

func TestRequestLogContextPropagatesGeneratedRequestID(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := echo.New()
	e.Use(middleware.RequestID())
	e.Use(requestLogContext(logger))
	e.GET("/", func(c echo.Context) error {
		logging.FromContext(c.Request().Context()).DebugContext(c.Request().Context(), "inside request")
		return c.NoContent(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	e.ServeHTTP(response, request)

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode log record: %v (%q)", err, output.String())
	}
	want := response.Header().Get(echo.HeaderXRequestID)
	if want == "" {
		t.Fatal("Echo did not generate a response request ID")
	}
	if got := record["request_id"]; got != want {
		t.Fatalf("request_id = %#v, want generated ID %q", got, want)
	}
}
