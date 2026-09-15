package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/anton-povarov/memoryd/server/api"
	"github.com/anton-povarov/memoryd/server/internal/config"
	"github.com/anton-povarov/memoryd/server/internal/httpapi"
	"github.com/anton-povarov/memoryd/server/internal/server"
	"github.com/anton-povarov/memoryd/server/internal/vault"
)

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	return newTestServerAt(t, t.TempDir())
}

func newTestServerAt(t *testing.T, root string) http.Handler {
	t.Helper()
	cfg := config.Defaults()
	cfg.Storage.DataDir = root
	cfg.Storage.DatabasePath = filepath.Join(root, "memoryd.sqlite")
	cfg.Storage.BlobDir = filepath.Join(root, "blobs")
	cfg.Storage.UploadDir = filepath.Join(root, "uploads")
	v, err := vault.Open(
		t.Context(),
		cfg.Storage.DatabasePath,
		cfg.Storage.BlobDir,
		cfg.Storage.UploadDir,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := server.New(
		cfg,
		logger,
		httpapi.NewHandler("test", cfg.Storage.DataDir, v),
	)
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler()
}

func TestImportReturnsServerErrorDetails(t *testing.T) {
	root := t.TempDir()
	handler := newTestServerAt(t, root)
	if err := os.RemoveAll(filepath.Join(root, "uploads")); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "memory.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("%PDF-1.7\ncontent\n%%EOF\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v0/memories/import", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var problem api.Error
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Code != "import_failed" ||
		!strings.Contains(problem.Message, "create temporary Blob") {
		t.Fatalf("import error = %#v", problem)
	}
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
		handler.ServeHTTP(
			recorder,
			httptest.NewRequest(http.MethodGet, test.path, nil),
		)
		if recorder.Code != test.status {
			t.Errorf(
				"GET %s status = %d, want %d; body=%s",
				test.path, recorder.Code, test.status, recorder.Body.String(),
			)
		}
		if !strings.Contains(
			recorder.Header().Get("Content-Type"),
			test.contentType,
		) {
			t.Errorf(
				"GET %s Content-Type = %q, want %q",
				test.path,
				recorder.Header().Get("Content-Type"),
				test.contentType,
			)
		}
	}
}

func TestBrowseStartsEmptyAndSearchStubEchoesQuery(t *testing.T) {
	handler := newTestServer(t)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/api/v0/memories", nil),
	)
	if recorder.Code != http.StatusOK ||
		recorder.Body.String() != "{\"items\":[]}\n" {
		t.Fatalf(
			"browse response: status=%d body=%s",
			recorder.Code,
			recorder.Body.String(),
		)
	}

	recorder = httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v0/search",
		strings.NewReader(`{"query":"Tasleem 2026"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf(
			"search response: status=%d body=%s",
			recorder.Code,
			recorder.Body.String(),
		)
	}
	var response struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil ||
		response.Query != "Tasleem 2026" {
		t.Fatalf("search body = %s, error = %v", recorder.Body.String(), err)
	}
}

func TestBrowseUsesNextCursorWithoutRepeatingMemories(t *testing.T) {
	handler := newTestServer(t)
	for index := 0; index < 3; index++ {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile(
			"file",
			fmt.Sprintf("memory-%d.pdf", index),
		)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprintf(part, "%%PDF-1.7\nMemory %d\n%%%%EOF\n", index)
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v0/memories/import",
			&body,
		)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf(
				"import %d: status=%d body=%s",
				index,
				response.Code,
				response.Body.String(),
			)
		}
	}

	first := browsePage(t, handler, "/api/v0/memories?limit=2")
	for _, item := range first.Items {
		if !regexp.MustCompile(`^sha256-[0-9a-f]{64}$`).MatchString(item.BlobHash) {
			t.Fatalf("browse Blobref = %q", item.BlobHash)
		}
	}
	if len(first.Items) != 2 || first.NextCursor == nil ||
		*first.NextCursor == "" {
		t.Fatalf("first page = %#v, want two items and a next cursor", first)
	}
	second := browsePage(
		t,
		handler,
		"/api/v0/memories?limit=2&cursor="+url.QueryEscape(*first.NextCursor),
	)
	if len(second.Items) != 1 || second.NextCursor != nil {
		t.Fatalf("second page = %#v, want final one-item page", second)
	}
	if !regexp.MustCompile(`^sha256-[0-9a-f]{64}$`).MatchString(second.Items[0].BlobHash) {
		t.Fatalf("next-page Blobref = %q", second.Items[0].BlobHash)
	}
	for _, earlier := range first.Items {
		if second.Items[0].Id == earlier.Id {
			t.Fatalf("Memory %s repeated across pages", earlier.Id)
		}
	}
}

func browsePage(
	t *testing.T,
	handler http.Handler,
	path string,
) api.MemoryPage {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf(
			"browse %s: status=%d body=%s",
			path,
			response.Code,
			response.Body.String(),
		)
	}
	var page api.MemoryPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestImportStreamsCompletionThenDownloadsExactBlob(t *testing.T) {
	handler := newTestServer(t)
	want := []byte("%PDF-1.7\nHTTP vertical slice\n%%EOF\n")
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "../unsafe/example.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(want); err != nil {
		t.Fatal(err)
	}
	_ = writer.WriteField("full_path", "/original/example.pdf")
	_ = writer.WriteField("filesystem_modified_at", "2026-09-15T10:11:12Z")
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v0/memories/import",
		&body,
	)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(
		recorder.Header().Get("Content-Type"),
		"text/event-stream",
	) ||
		!strings.Contains(recorder.Body.String(), "event: import_started") ||
		!strings.Contains(recorder.Body.String(), "event: understanding_progress") ||
		!strings.Contains(recorder.Body.String(), "event: import_completed") {
		t.Fatalf("unexpected event stream: %s", recorder.Body.String())
	}

	match := regexp.MustCompile(`"id":"([0-9a-f-]{36})"`).
		FindStringSubmatch(recorder.Body.String())
	if len(match) != 2 {
		t.Fatalf(
			"completion event has no Memory ID: %s",
			recorder.Body.String(),
		)
	}
	if !regexp.MustCompile(`"blob_hash":"sha256-[0-9a-f]{64}"`).
		MatchString(recorder.Body.String()) {
		t.Fatalf("completion event has no canonical Blobref: %s", recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(
		recorder,
		httptest.NewRequest(
			http.MethodGet,
			"/api/v0/memories/"+match[1]+"/content",
			nil,
		),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf(
			"download status=%d body=%s",
			recorder.Code,
			recorder.Body.String(),
		)
	}
	if !bytes.Equal(recorder.Body.Bytes(), want) {
		t.Fatalf("download = %q, want %q", recorder.Body.Bytes(), want)
	}
	if got := recorder.Header().
		Get("Content-Type"); !strings.Contains(
		got,
		"application/pdf",
	) {
		t.Fatalf("Content-Type = %q", got)
	}
	if _, parameters, err := mime.ParseMediaType(
		recorder.Header().Get("Content-Disposition"),
	); err != nil || parameters["filename"] != "example.pdf" {
		t.Fatalf(
			"Content-Disposition = %q, error = %v",
			recorder.Header().Get("Content-Disposition"), err,
		)
	}
	if got := recorder.Header().
		Get("ETag"); !regexp.MustCompile(`^"sha256-[0-9a-f]{64}"$`).
		MatchString(got) {
		t.Fatalf("ETag = %q", got)
	}
	if got := recorder.Header().
		Get("Content-Length"); got != strconv.Itoa(
		len(want),
	) {
		t.Fatalf("Content-Length = %q", got)
	}
}

func TestMarkdownImportDownloadAndDuplicate(t *testing.T) {
	handler := newTestServer(t)
	want := []byte("# Notes\nA café visit.\n")
	upload := func() *httptest.ResponseRecorder {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", "notes.md")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(want); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v0/memories/import",
			&body,
		)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	imported := upload()
	if imported.Code != http.StatusOK ||
		!strings.Contains(imported.Body.String(), `"media_type":"text/markdown"`) {
		t.Fatalf("import status=%d body=%s", imported.Code, imported.Body.String())
	}
	match := regexp.MustCompile(`"id":"([0-9a-f-]{36})"`).
		FindStringSubmatch(imported.Body.String())
	if len(match) != 2 {
		t.Fatalf("completion event has no Memory ID: %s", imported.Body.String())
	}

	download := httptest.NewRecorder()
	handler.ServeHTTP(
		download,
		httptest.NewRequest(
			http.MethodGet,
			"/api/v0/memories/"+match[1]+"/content",
			nil,
		),
	)
	if download.Code != http.StatusOK ||
		!bytes.Equal(download.Body.Bytes(), want) ||
		!strings.Contains(download.Header().Get("Content-Type"), "text/markdown") {
		t.Fatalf(
			"download status=%d type=%q body=%q",
			download.Code,
			download.Header().Get("Content-Type"),
			download.Body.Bytes(),
		)
	}
	if disposition := download.Header().
		Get("Content-Disposition"); !strings.Contains(
		disposition,
		"notes.md",
	) {
		t.Fatalf("Content-Disposition = %q", disposition)
	}

	duplicate := upload()
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
	var problem api.Error
	if err := json.Unmarshal(duplicate.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Code != "duplicate_memory" ||
		problem.ExistingMemory == nil || problem.ExistingMemory.Id.String() != match[1] {
		t.Fatalf("duplicate response = %#v", problem)
	}
}
