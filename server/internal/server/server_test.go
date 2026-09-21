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
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/anton-povarov/memoryd/server/internal/api"
	"github.com/anton-povarov/memoryd/server/internal/config"
	"github.com/anton-povarov/memoryd/server/internal/server"
	"github.com/anton-povarov/memoryd/server/internal/vault"
)

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	return newTestServerAt(t, t.TempDir())
}

func newTestServerAt(t *testing.T, root string) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return newTestServerAtWithLogger(t, root, logger)
}

func newTestServerAtWithLogger(
	t *testing.T,
	root string,
	logger *slog.Logger,
) http.Handler {
	t.Helper()
	cfg := config.Defaults()
	cfg.Storage.DataDir = root
	cfg.Storage.DatabasePath = filepath.Join(root, "memoryd.sqlite")
	cfg.Storage.BlobDir = filepath.Join(root, "blobs")
	cfg.Storage.UploadDir = filepath.Join(root, "uploads")
	vlt, err := vault.Open(
		t.Context(),
		cfg.Storage.DatabasePath,
		cfg.Storage.BlobDir,
		cfg.Storage.UploadDir,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vlt.Close() })

	s, err := server.New("test version", cfg, logger, vlt)
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler()
}

func TestRequestIDsAndFinalStatusLogging(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler := newTestServerAtWithLogger(t, t.TempDir(), logger)

	request := httptest.NewRequest(http.MethodGet, "/api/v0/livez", nil)
	request.Header.Set("X-Request-ID", "client-request-id")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Header().Get("X-Request-ID") != "client-request-id" {
		t.Fatalf(
			"X-Request-ID = %q, want client-request-id",
			response.Header().Get("X-Request-ID"),
		)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v0/livez", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/v0/livez status = %d", response.Code)
	}
	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("generated X-Request-ID is empty")
	}

	if !strings.Contains(logs.String(), `"msg":"HTTP request"`) ||
		!strings.Contains(logs.String(), `"status":405`) {
		t.Fatalf("request log does not contain final 405 status: %s", logs.String())
	}
}

func writeImportContextPart(writer *multipart.Writer, value string) error {
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="import_context"`)
	header.Set("Content-Type", "application/json")
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	_, err = io.WriteString(part, value)
	return err
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

func TestImportRejectsMalformedContextTimestamp(t *testing.T) {
	handler := newTestServer(t)
	for _, field := range []string{"filesystem_created_at", "filesystem_modified_at"} {
		t.Run(field, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			part, err := writer.CreateFormFile("file", "memory.pdf")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write([]byte("%PDF-1.7\ncontent\n%%EOF\n")); err != nil {
				t.Fatal(err)
			}
			if err := writeImportContextPart(
				writer,
				fmt.Sprintf(`{%q:"not-a-time"}`, field),
			); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/api/v0/memories/import", &body)
			request.Header.Set("Content-Type", writer.FormDataContentType())
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			var problem api.Error
			if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
				t.Fatal(err)
			}
			if problem.Code != "invalid_import_context" ||
				!strings.Contains(problem.Message, field) {
				t.Fatalf("import error = %#v", problem)
			}
		})
	}
}

func TestImportRequiresFilePartFilename(t *testing.T) {
	handler := newTestServer(t)
	for _, test := range []struct {
		name        string
		disposition string
	}{
		{name: "missing", disposition: `form-data; name="file"`},
		{name: "blank", disposition: `form-data; name="file"; filename="  "`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			header := make(textproto.MIMEHeader)
			header.Set("Content-Disposition", test.disposition)
			part, err := writer.CreatePart(header)
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
			if response.Code != http.StatusBadRequest ||
				!strings.Contains(response.Body.String(), "invalid_file") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestImportRejectsRequestBeyondBoundBeforeReadingBody(t *testing.T) {
	handler := newTestServer(t)
	body := &trackingReader{content: strings.NewReader("must not be read")}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v0/memories/import",
		body,
	)
	request.ContentLength = server.MaxImportRequestBytes + 1
	request.Header.Set("Content-Type", "multipart/form-data; boundary=unused")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if body.read {
		t.Fatal("oversized request body was read")
	}
	var problem api.Error
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	wantMessage := fmt.Sprintf(
		"upload limit: %d bytes",
		server.MaxImportRequestBytes,
	)
	if problem.Code != "blob_too_large" || problem.Message != wantMessage {
		t.Fatalf("problem = %#v", problem)
	}
}

type trackingReader struct {
	content io.Reader
	read    bool
}

func (reader *trackingReader) Read(destination []byte) (int, error) {
	reader.read = true
	return reader.content.Read(destination)
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
		{path: "/", status: http.StatusOK, contentType: "text/html"},
		{path: "/assets/app.css", status: http.StatusOK, contentType: "text/css"},
		{
			path:        "/assets/app.js",
			status:      http.StatusOK,
			contentType: "text/javascript",
		},
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

func TestBrowseStartsEmptyAndUnsupportedSearchIsAbsent(t *testing.T) {
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
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("search status=%d body=%s", recorder.Code, recorder.Body.String())
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
		if response.Code != http.StatusCreated {
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

func TestImportReturnsCommittedMemoryThenDownloadsExactBlob(t *testing.T) {
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
	if err := writeImportContextPart(
		writer,
		`{"full_path":"/original/example.pdf",`+
			`"relative_path":"folder/example.pdf",`+
			`"filesystem_modified_at":"2026-09-15T10:11:12Z"}`,
	); err != nil {
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
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("unexpected Content-Type: %s", recorder.Header().Get("Content-Type"))
	}

	match := regexp.MustCompile(`"id":"([0-9a-f-]{36})"`).
		FindStringSubmatch(recorder.Body.String())
	if len(match) != 2 {
		t.Fatalf(
			"response has no Memory ID: %s",
			recorder.Body.String(),
		)
	}
	if !regexp.MustCompile(`"blob_hash":"sha256-[0-9a-f]{64}"`).
		MatchString(recorder.Body.String()) {
		t.Fatalf("completion event has no canonical Blobref: %s", recorder.Body.String())
	}
	assertImportContextDetail(t, handler, match[1])
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

func assertImportContextDetail(t *testing.T, handler http.Handler, memoryID string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/api/v0/memories/"+memoryID, nil),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var detail api.MemoryDetail
	if err := json.Unmarshal(recorder.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.ImportContext.OriginalFilename != "example.pdf" ||
		detail.ImportContext.FullPath == nil ||
		*detail.ImportContext.FullPath != "/original/example.pdf" ||
		detail.ImportContext.RelativePath == nil ||
		*detail.ImportContext.RelativePath != "folder/example.pdf" ||
		detail.ImportContext.FilesystemCreatedAt != nil ||
		detail.ImportContext.FilesystemModifiedAt == nil {
		t.Fatalf("Import Context = %#v", detail.ImportContext)
	}
}

func TestMarkdownImportDownloadAndDuplicate(t *testing.T) {
	handler := newTestServer(t)
	want := []byte("# Notes\nA café visit.\n")
	upload := func() *httptest.ResponseRecorder {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", `form-data; name="file"; filename="notes.md"`)
		header.Set("Content-Type", "text/markdown")
		part, err := writer.CreatePart(header)
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
	if imported.Code != http.StatusCreated ||
		!strings.Contains(imported.Body.String(), `"media_type":"text/markdown"`) {
		t.Fatalf("import status=%d body=%s", imported.Code, imported.Body.String())
	}
	match := regexp.MustCompile(`"id":"([0-9a-f-]{36})"`).
		FindStringSubmatch(imported.Body.String())
	if len(match) != 2 {
		t.Fatalf("response has no Memory ID: %s", imported.Body.String())
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

func TestUnknownBlobUsesDeclaredMediaTypeAndRejectsMalformedFallback(t *testing.T) {
	handler := newTestServer(t)
	upload := func(content []byte, contentType string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", `form-data; name="file"; filename="notes.bin"`)
		header.Set("Content-Type", contentType)
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(content); err != nil {
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
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	content := []byte{0x00, 0x01, 0x02, 0xff}
	imported := upload(content, "Application/X-Notebook; charset=UTF-8")
	if imported.Code != http.StatusCreated ||
		!strings.Contains(imported.Body.String(), `"media_type":"application/x-notebook"`) {
		t.Fatalf("import status=%d body=%s", imported.Code, imported.Body.String())
	}
	match := regexp.MustCompile(`"id":"([0-9a-f-]{36})"`).
		FindStringSubmatch(imported.Body.String())
	if len(match) != 2 {
		t.Fatalf("response has no Memory ID: %s", imported.Body.String())
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
		!bytes.Equal(download.Body.Bytes(), content) ||
		download.Header().Get("Content-Type") != "application/x-notebook" {
		t.Fatalf(
			"download status=%d type=%q body=%q",
			download.Code,
			download.Header().Get("Content-Type"),
			download.Body.Bytes(),
		)
	}

	invalid := upload([]byte{0x00, 0x01, 0x03, 0xff}, "nonsense")
	if invalid.Code != http.StatusBadRequest ||
		!strings.Contains(invalid.Body.String(), `"code":"invalid_media_type"`) {
		t.Fatalf("invalid type status=%d body=%s", invalid.Code, invalid.Body.String())
	}
}
