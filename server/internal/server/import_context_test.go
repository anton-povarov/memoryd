package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anton-povarov/memoryd/server/internal/api"
	"github.com/anton-povarov/memoryd/server/internal/config"
	"github.com/anton-povarov/memoryd/server/internal/vault"
)

func TestParseImportContext(t *testing.T) {
	t.Parallel()

	t.Run("valid timestamps and filename", func(t *testing.T) {
		t.Parallel()
		context, err := parseImportContext(
			`{"relative_path":"notes/note.txt","full_path":"/archive/note.txt","filesystem_created_at":"2024-01-02T03:04:05.123456789Z","filesystem_modified_at":"2024-02-03T04:05:06+04:00"}`,
			"uploaded.txt",
		)
		if err != nil {
			t.Fatal(err)
		}
		if context.OriginalFilename != "uploaded.txt" ||
			context.RelativePath != "notes/note.txt" ||
			context.FullPath != "/archive/note.txt" {
			t.Fatalf("context fields = %#v", context)
		}
		createdAt, err := time.Parse(time.RFC3339Nano, "2024-01-02T03:04:05.123456789Z")
		if err != nil {
			t.Fatal(err)
		}
		modifiedAt, err := time.Parse(time.RFC3339Nano, "2024-02-03T04:05:06+04:00")
		if err != nil {
			t.Fatal(err)
		}
		if context.FilesystemCreated == nil || !context.FilesystemCreated.Equal(createdAt) {
			t.Fatalf("filesystem created at = %v, want %v", context.FilesystemCreated, createdAt)
		}
		if context.FilesystemModified == nil || !context.FilesystemModified.Equal(modifiedAt) {
			t.Fatalf("filesystem modified at = %v, want %v", context.FilesystemModified, modifiedAt)
		}
	})

	t.Run("empty optional timestamps", func(t *testing.T) {
		t.Parallel()
		context, err := parseImportContext(
			`{"filesystem_created_at":"","filesystem_modified_at":null}`,
			"uploaded.txt",
		)
		if err != nil {
			t.Fatal(err)
		}
		if context.FilesystemCreated != nil || context.FilesystemModified != nil {
			t.Fatalf("optional timestamps = %#v", context)
		}
	})

	tests := []struct {
		name string
		json string
		want string
	}{
		{
			name: "invalid timestamp",
			json: `{"filesystem_created_at":"yesterday"}`,
			want: "filesystem_created_at",
		},
		{
			name: "unknown field",
			json: `{"media_type":"text/plain"}`,
			want: "unknown field",
		},
		{
			name: "trailing JSON value",
			json: `{} {}`,
			want: "one JSON value",
		},
		{
			name: "non-object JSON",
			json: `null`,
			want: "JSON object",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseImportContext(test.json, "uploaded.txt")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want it to contain %q", err, test.want)
			}
		})
	}
}

func TestImportMemoryHTTPStoresContextAndSeparatesBlobFields(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	memoryVault, err := vault.Open(
		context.Background(), logger,
		filepath.Join(root, "memoryd.sqlite"),
		filepath.Join(root, "blobs"),
		filepath.Join(root, "uploads"),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memoryVault.Close() })
	s, err := New("test", config.Defaults(), logger, memoryVault)
	if err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	contextPart, err := form.CreateFormField("import_context")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(
		contextPart,
		`{"full_path":"/archive/uploaded.pdf","filesystem_modified_at":"2026-09-15T10:11:12+04:00"}`,
	); err != nil {
		t.Fatal(err)
	}
	filePart, err := form.CreateFormFile("file", "uploaded.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(filePart, "%PDF-1.7\nfixture\n%%EOF\n"); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v0/memories/import", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("import response = %d %s", response.Code, response.Body.String())
	}
	var summary api.MemorySummary
	if err := json.Unmarshal(response.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.OriginalFilename != "uploaded.pdf" || summary.MediaType != "application/pdf" ||
		summary.ByteSize != int64(len("%PDF-1.7\nfixture\n%%EOF\n")) {
		t.Fatalf("imported Memory = %#v", summary)
	}

	detailRequest := httptest.NewRequest(
		http.MethodGet, "/api/v0/memories/"+summary.Id.String(), nil,
	)
	detailResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(detailResponse, detailRequest)
	if detailResponse.Code != http.StatusOK {
		t.Fatalf("detail response = %d %s", detailResponse.Code, detailResponse.Body.String())
	}
	var detail struct {
		ImportContext map[string]any `json:"import_context"`
	}
	if err := json.Unmarshal(detailResponse.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.ImportContext["full_path"] != "/archive/uploaded.pdf" ||
		detail.ImportContext["filesystem_modified_at"] != "2026-09-15T06:11:12Z" {
		t.Fatalf("stored Import Context = %#v", detail.ImportContext)
	}
	for _, field := range []string{"content_hash", "media_type", "byte_size"} {
		if _, exists := detail.ImportContext[field]; exists {
			t.Fatalf("%q leaked into Import Context: %#v", field, detail.ImportContext)
		}
	}
}
