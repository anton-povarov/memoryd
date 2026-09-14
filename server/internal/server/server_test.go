package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anton-povarov/memoryd/server/internal/config"
	"github.com/anton-povarov/memoryd/server/internal/httpapi"
	"github.com/anton-povarov/memoryd/server/internal/server"
)

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	cfg := config.Defaults()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := server.New(cfg, logger, httpapi.NewHandler("test", cfg.Storage.DataDir))
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler()
}

func TestHealthAndDocumentation(t *testing.T) {
	handler := newTestServer(t)
	for _, test := range []struct {
		path        string
		status      int
		contentType string
	}{
		{path: "/api/v0/livez", status: http.StatusOK, contentType: "application/json"},
		{path: "/api/v0/readyz", status: http.StatusOK, contentType: "application/json"},
		{path: "/openapi.yaml", status: http.StatusOK, contentType: "text/yaml"},
		{path: "/openapi.json", status: http.StatusOK, contentType: "application/json"},
		{path: "/docs/", status: http.StatusOK, contentType: "text/html"},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
		if recorder.Code != test.status {
			t.Errorf("GET %s status = %d, want %d; body=%s", test.path, recorder.Code, test.status, recorder.Body.String())
		}
		if !strings.Contains(recorder.Header().Get("Content-Type"), test.contentType) {
			t.Errorf("GET %s Content-Type = %q, want %q", test.path, recorder.Header().Get("Content-Type"), test.contentType)
		}
	}
}

func TestBrowseAndSearchStubs(t *testing.T) {
	handler := newTestServer(t)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v0/memories", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "memoryd-v0-example.pdf") {
		t.Fatalf("browse response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v0/search", strings.NewReader(`{"query":"Tasleem 2026"}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("search response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Query != "Tasleem 2026" {
		t.Fatalf("search body = %s, error = %v", recorder.Body.String(), err)
	}
}

func TestImportStubReturnsEvents(t *testing.T) {
	handler := newTestServer(t)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "example.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("stub")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v0/memories/import", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(recorder.Body.String(), "event: import_completed") {
		t.Fatalf("unexpected event stream: %s", recorder.Body.String())
	}
}
