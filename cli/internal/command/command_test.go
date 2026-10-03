package command

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anton-povarov/memoryd/cli/internal/api"
	"github.com/google/uuid"
)

func TestPutReturnsCommittedMemoryAndTreatsDuplicateAsSuccess(t *testing.T) {
	memoryID := uuid.MustParse("2d6f4d1a-4d62-4ef3-9b2c-6aa7f1f0d6c2")
	want := []byte("%PDF-1.7\nCLI upload\n%%EOF\n")
	path := filepath.Join(t.TempDir(), "upload.pdf")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v0/memories/import" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		got, _ := io.ReadAll(file)
		var importContext struct {
			FullPath             string `json:"full_path"`
			FilesystemModifiedAt string `json:"filesystem_modified_at"`
		}
		if err := json.Unmarshal(
			[]byte(r.FormValue("import_context")),
			&importContext,
		); err != nil {
			t.Error(err)
		}
		if header.Filename != "upload.pdf" || !bytes.Equal(got, want) ||
			header.Header.Get("Content-Type") != "application/pdf" ||
			!filepath.IsAbs(importContext.FullPath) || importContext.FilesystemModifiedAt == "" {
			t.Errorf(
				"multipart filename=%q bytes=%q full_path=%q modified=%q",
				header.Filename, got, importContext.FullPath,
				importContext.FilesystemModifiedAt,
			)
		}
		if requests.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(
				testMemorySummary(memoryID, int64(len(want)), "upload.pdf"),
			)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(api.Error{
			Code: "duplicate_memory", Details: nil, Message: "duplicate",
			ExistingMemory: &api.DuplicateMemory{
				Id: memoryID,
			},
		})
	}))
	defer server.Close()

	for attempt := 1; attempt <= 2; attempt++ {
		var stdout, stderr bytes.Buffer
		if err := Put(t.Context(), server.URL, path, &stdout, &stderr); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if stdout.String() != memoryID.String()+"\n" {
			t.Fatalf("attempt %d stdout = %q", attempt, stdout.String())
		}
	}
}

func TestPutDeclaresMarkdownMediaType(t *testing.T) {
	memoryID := uuid.New()
	path := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(path, []byte("# Notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		if got := header.Header.Get("Content-Type"); got != "text/markdown" {
			t.Errorf("file Content-Type = %q, want text/markdown", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(api.Error{
			Code: "duplicate_memory", Details: nil, Message: "duplicate",
			ExistingMemory: &api.DuplicateMemory{
				Id: memoryID,
			},
		})
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if err := Put(t.Context(), server.URL, path, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != memoryID.String()+"\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestPutReportsServerImportFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upload.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7\ncontent\n%%EOF\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(api.Error{
			Code:           "import_failed",
			Details:        nil,
			ExistingMemory: nil,
			Message:        "create temporary Blob: open data/uploads/.import-123: no such file or directory",
		})
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	err := Put(t.Context(), server.URL, path, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500 Internal Server Error") ||
		!strings.Contains(err.Error(), "create temporary Blob:") ||
		!strings.Contains(err.Error(), "no such file or directory") {
		t.Fatalf("Put error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestPutKeepsStatusFromLegacyGenericServerError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upload.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7\ncontent\n%%EOF\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"message":"Internal Server Error"}`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	err := Put(t.Context(), server.URL, path, &stdout, &stderr)
	if err == nil || err.Error() != "import failed with HTTP 500 Internal Server Error" {
		t.Fatalf("Put error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestGetUsesSafeServerFilenameAndDownloadsAtomically(t *testing.T) {
	memoryID := uuid.MustParse("2d6f4d1a-4d62-4ef3-9b2c-6aa7f1f0d6c2")
	want := []byte("%PDF-1.7\ndownload\n%%EOF\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", `attachment; filename="../../restored.pdf"`)
		w.Header().Set("Content-Length", fmt.Sprint(len(want)))
		_, _ = w.Write(want)
	}))
	defer server.Close()

	directory := t.TempDir()
	t.Chdir(directory)
	if err := Get(
		t.Context(),
		server.URL,
		memoryID,
		"",
		false,
		io.Discard,
		io.Discard,
	); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(directory, "restored.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("download = %q, want %q", got, want)
	}
	if err := Get(
		t.Context(),
		server.URL,
		memoryID,
		"",
		false,
		io.Discard,
		io.Discard,
	); err == nil {
		t.Fatal("existing destination was overwritten without --force")
	}
	matches, _ := filepath.Glob(filepath.Join(directory, ".restored.pdf.tmp-*"))
	if len(matches) != 0 {
		t.Fatalf("temporary files remain: %v", matches)
	}
}

func TestInfoReturnsMemoryDetailsWithoutDownloadingContent(t *testing.T) {
	memoryID := uuid.MustParse("2d6f4d1a-4d62-4ef3-9b2c-6aa7f1f0d6c2")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v0/memories/"+memoryID.String() {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(testMemoryDetail(memoryID, 42, "memory.pdf"))
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := Info(t.Context(), server.URL, memoryID, &stdout); err != nil {
		t.Fatal(err)
	}
	var detail api.MemoryDetail
	if err := json.Unmarshal(stdout.Bytes(), &detail); err != nil {
		t.Fatalf("stdout is not JSON: %v", err)
	}
	if detail.Memory.Id != memoryID || detail.Memory.OriginalFilename != "memory.pdf" ||
		requests.Load() != 1 {
		t.Fatalf("detail=%+v requests=%d", detail.Memory, requests.Load())
	}
}

func TestInfoReportsNotFound(t *testing.T) {
	memoryID := uuid.MustParse("2d6f4d1a-4d62-4ef3-9b2c-6aa7f1f0d6c2")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(api.Error{
			Code: "memory_not_found", Details: nil,
			ExistingMemory: nil, Message: "Memory does not exist",
		})
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := Info(t.Context(), server.URL, memoryID, &stdout)
	if err == nil || !strings.Contains(err.Error(), "memory_not_found: Memory does not exist") ||
		stdout.Len() != 0 {
		t.Fatalf("error=%v stdout=%q", err, stdout.String())
	}
}

func TestDeleteDeletesMemoryAndPrintsItsID(t *testing.T) {
	memoryID := uuid.MustParse("2d6f4d1a-4d62-4ef3-9b2c-6aa7f1f0d6c2")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodDelete ||
			r.URL.Path != "/api/v0/memories/"+memoryID.String() {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if err := Delete(t.Context(), server.URL, memoryID, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != memoryID.String()+"\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "deleting Memory "+memoryID.String()) {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
}

func TestDeleteReportsNotFound(t *testing.T) {
	memoryID := uuid.MustParse("2d6f4d1a-4d62-4ef3-9b2c-6aa7f1f0d6c2")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(api.Error{
			Code: "memory_not_found", Details: nil,
			ExistingMemory: nil, Message: "Memory does not exist",
		})
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	err := Delete(t.Context(), server.URL, memoryID, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "memory_not_found: Memory does not exist") ||
		stdout.Len() != 0 {
		t.Fatalf("error=%v stdout=%q", err, stdout.String())
	}
}

func testMemorySummary(id uuid.UUID, size int64, filename string) api.MemorySummary {
	return api.MemorySummary{
		BlobHash: "sha256-" + strings.Repeat("a", 64),
		ByteSize: size, Id: id, ImportedAt: time.Time{},
		MediaType: "application/pdf", OriginalCreatedAt: nil,
		OriginalFilename: filename, OriginalModifiedAt: nil,
	}
}

func testMemoryDetail(id uuid.UUID, size int64, filename string) api.MemoryDetail {
	return api.MemoryDetail{
		ContentUrl: nil,
		ImportContext: api.ImportContext{
			FilesystemCreatedAt: nil, FilesystemModifiedAt: nil,
			FullPath:         nil,
			OriginalFilename: filename, RelativePath: nil,
		},
		Memory: testMemorySummary(id, size, filename),
	}
}

func TestSearchFollowsPagesUntilRequestedCount(t *testing.T) {
	hits := make([]api.SearchHit, 205)
	for index := range hits {
		id := uuid.New()
		hits[index] = api.SearchHit{
			Memory:  testMemorySummary(id, int64(index), fmt.Sprintf("memory-%03d.txt", index)),
			Excerpt: []api.SearchExcerptPart{},
		}
	}
	offsets := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") != "needle" {
			http.Error(w, "unexpected query", http.StatusBadRequest)
			return
		}
		offset := offsets[r.URL.Query().Get("cursor")]
		end := offset + 100
		if end > len(hits) {
			end = len(hits)
		}
		var next *string
		if end < len(hits) {
			token := fmt.Sprintf("next-page-%d", end)
			offsets[token] = end
			next = &token
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(api.SearchPage{
			QueryPlan:  api.SearchQueryPlan{Query: "needle", Terms: []string{"needle"}},
			Total:      int64(len(hits)),
			Items:      hits[offset:end],
			NextCursor: next,
		})
	}))
	defer server.Close()

	var output bytes.Buffer
	if err := Search(t.Context(), server.URL, "needle", 201, &output); err != nil {
		t.Fatal(err)
	}
	var result api.SearchPage
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("decode search output: %v", err)
	}
	if len(result.Items) != 201 || result.Total != 205 ||
		result.QueryPlan.Query != "needle" {
		t.Fatalf("bounded search result = total %d, items %d, plan %#v",
			result.Total, len(result.Items), result.QueryPlan)
	}
	for index, hit := range result.Items {
		if hit.Memory.Id != hits[index].Memory.Id {
			t.Fatalf("result %d = %s, want %s", index, hit.Memory.Id, hits[index].Memory.Id)
		}
	}
	output.Reset()
	if err := SearchAll(t.Context(), server.URL, "needle", &output); err != nil {
		t.Fatal(err)
	}
	result = api.SearchPage{}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("decode all-search output: %v", err)
	}
	if len(result.Items) != len(hits) || result.Total != int64(len(hits)) ||
		result.QueryPlan.Query != "needle" || result.NextCursor != nil {
		t.Fatalf("all search = total %d, items %d, plan %#v, cursor %v",
			result.Total, len(result.Items), result.QueryPlan, result.NextCursor)
	}
	for index, hit := range result.Items {
		if hit.Memory.Id != hits[index].Memory.Id {
			t.Fatalf("all result %d = %s, want %s", index, hit.Memory.Id, hits[index].Memory.Id)
		}
	}
}
