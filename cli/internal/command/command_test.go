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

	"github.com/anton-povarov/memoryd/cli/internal/api"
	"github.com/google/uuid"
)

func TestPutParsesGeneratedEventsAndTreatsDuplicateAsSuccess(t *testing.T) {
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
		if header.Filename != "upload.pdf" || !bytes.Equal(got, want) || !filepath.IsAbs(r.FormValue("full_path")) || r.FormValue("filesystem_modified_at") == "" {
			t.Errorf("multipart filename=%q bytes=%q full_path=%q modified=%q", header.Filename, got, r.FormValue("full_path"), r.FormValue("filesystem_modified_at"))
		}
		if requests.Add(1) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: import_started\ndata: {\"event\":\"import_started\",\"memory_id\":%q,\"phase\":\"accepted\"}\n\n", memoryID)
			fmt.Fprintf(w, "event: understanding_progress\ndata: {\"event\":\"understanding_progress\",\"memory_id\":%q,\"phase\":\"stub\",\"message\":\"working\"}\n\n", memoryID)
			completion := api.ImportCompletedEvent{Event: api.ImportCompleted, Memory: api.MemorySummary{Id: memoryID, BlobHash: strings.Repeat("a", 64), MediaType: "application/pdf", ByteSize: int64(len(want)), OriginalFilename: "upload.pdf", UnderstandingState: api.Done}}
			encoded, _ := json.Marshal(completion)
			fmt.Fprintf(w, "event: import_completed\ndata: %s\n\n", encoded)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(api.Error{Code: "duplicate_memory", Message: "duplicate", ExistingMemory: &api.DuplicateMemory{Id: memoryID, UnderstandingState: api.Done}})
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
		if attempt == 1 && !strings.Contains(stderr.String(), "stub") {
			t.Fatalf("progress stderr = %q", stderr.String())
		}
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
	if err := Get(t.Context(), server.URL, memoryID, "", false, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(directory, "restored.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("download = %q, want %q", got, want)
	}
	if err := Get(t.Context(), server.URL, memoryID, "", false, io.Discard, io.Discard); err == nil {
		t.Fatal("existing destination was overwritten without --force")
	}
	matches, _ := filepath.Glob(filepath.Join(directory, ".restored.pdf.tmp-*"))
	if len(matches) != 0 {
		t.Fatalf("temporary files remain: %v", matches)
	}
}
