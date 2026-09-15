package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anton-povarov/memoryd/cli/internal/api"
	"github.com/google/uuid"
)

func TestSubcommandsUseGlobalServer(t *testing.T) {
	memoryID := uuid.MustParse("2d6f4d1a-4d62-4ef3-9b2c-6aa7f1f0d6c2")
	content := []byte("%PDF-1.7\nMemory\n%%EOF\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v0/memories/import":
			w.Header().Set("Content-Type", "text/event-stream")
			event := api.ImportCompletedEvent{
				Event:  api.ImportCompleted,
				Memory: testSummary(memoryID, int64(len(content)), "memory.pdf"),
			}
			encoded, _ := json.Marshal(event)
			fmt.Fprintf(w, "event: import_completed\ndata: %s\n\n", encoded)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v0/memories/"+memoryID.String():
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(api.MemoryDetail{
				ActiveRun: nil, ContentUrl: nil,
				Memory: testSummary(memoryID, int64(len(content)), "memory.pdf"),
				ImportContext: api.ImportContext{
					ByteSize: nil, ContentHash: nil,
					FilesystemCreatedAt: nil, FilesystemModifiedAt: nil,
					FullPath: nil, MediaType: nil,
					OriginalFilename: "memory.pdf", RelativePath: nil,
				},
				Facts: []api.Fact{}, DerivedContent: []api.DerivedContent{},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v0/memories/"+memoryID.String()+"/content":
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write(content)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "memory.pdf")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "put", args: []string{"put", path}, want: memoryID.String() + "\n"},
		{name: "info", args: []string{"info", memoryID.String()},
			want: "\"original_filename\": \"memory.pdf\""},
		{name: "get", args: []string{"get", "-o", "-", memoryID.String()}, want: string(content)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			args := append([]string{"--server=" + server.URL}, test.args...)
			if code := run(t.Context(), args, &stdout, &stderr); code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), test.want) {
				t.Fatalf("stdout=%q, want %q", stdout.String(), test.want)
			}
		})
	}
}

func TestSubcommandUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{}, {"unknown"}, {"get"}, {"get", "not-a-uuid"}, {"put"}, {"info"}, {"info", "not-a-uuid"},
		{"list", "-n", "0"}, {"list", "-n", "bad"}, {"list", "--all", "-n", "2"}, {"list", "extra"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(t.Context(), args, &stdout, &stderr); code != 2 ||
			stderr.Len() == 0 || stdout.Len() != 0 {
			t.Fatalf(
				"args=%v exit=%d stdout=%q stderr=%q",
				args,
				code,
				stdout.String(),
				stderr.String(),
			)
		}
	}
}

func TestListPaginationAndOutput(t *testing.T) {
	items := make([]api.MemorySummary, 103)
	for i := range items {
		items[i] = testSummary(uuid.New(), 12, fmt.Sprintf("memory-%d.pdf", i))
	}
	var limits []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v0/memories" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
		if err != nil || limit < 1 || limit > 100 {
			t.Errorf("invalid limit %q", r.URL.Query().Get("limit"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		limits = append(limits, limit)
		start := 0
		if value := r.URL.Query().Get("cursor"); value != "" {
			start, err = strconv.Atoi(value)
			if err != nil {
				t.Errorf("invalid cursor %q", value)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		end := min(start+limit, len(items))
		page := api.MemoryPage{Items: items[start:end], NextCursor: nil}
		if end < len(items) {
			cursor := strconv.Itoa(end)
			page.NextCursor = &cursor
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer server.Close()

	for _, test := range []struct {
		name       string
		args       []string
		wantCount  int
		wantLimits []int
		short      bool
	}{
		{name: "default", args: []string{"list"}, wantCount: 50,
			wantLimits: []int{50}, short: false},
		{name: "count over page cap", args: []string{"list", "-n", "101"},
			wantCount: 101, wantLimits: []int{100, 1}, short: false},
		{name: "all short", args: []string{"list", "--all", "--short"},
			wantCount: 103, wantLimits: []int{100, 100}, short: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			limits = nil
			var stdout, stderr bytes.Buffer
			args := append([]string{"--server=" + server.URL}, test.args...)
			if code := run(t.Context(), args, &stdout, &stderr); code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, stderr.String())
			}
			if fmt.Sprint(limits) != fmt.Sprint(test.wantLimits) {
				t.Fatalf("limits=%v, want %v", limits, test.wantLimits)
			}
			if test.short {
				lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
				if len(lines) != test.wantCount || lines[0] != items[0].Id.String() ||
					lines[len(lines)-1] != items[test.wantCount-1].Id.String() {
					t.Fatalf("IDs=%d first=%q last=%q", len(lines), lines[0], lines[len(lines)-1])
				}
			} else {
				var got []api.MemorySummary
				if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
					t.Fatalf("stdout is not JSON: %v", err)
				}
				if len(got) != test.wantCount || got[0].OriginalFilename != "memory-0.pdf" ||
					got[len(got)-1].OriginalFilename != fmt.Sprintf(
						"memory-%d.pdf",
						test.wantCount-1,
					) {
					t.Fatalf(
						"Memories=%d first=%q last=%q",
						len(got), got[0].OriginalFilename, got[len(got)-1].OriginalFilename,
					)
				}
			}
		})
	}
}

func testSummary(id uuid.UUID, size int64, filename string) api.MemorySummary {
	return api.MemorySummary{
		ActiveRunId: nil, BlobHash: strings.Repeat("a", 64),
		ByteSize: size, Id: id, ImportedAt: time.Time{},
		MediaType: "application/pdf", OriginalCreatedAt: nil,
		OriginalFilename: filename, OriginalModifiedAt: nil,
		UnderstandingState: api.Done,
	}
}
