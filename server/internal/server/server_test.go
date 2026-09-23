package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anton-povarov/memoryd/server/internal/config"
	"github.com/anton-povarov/memoryd/server/internal/vault"
)

func TestNewAcceptsStandardSlogLogger(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	s, err := New("test", config.Defaults(), logger, nil)
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v0/livez", nil)
	request.Header.Set(requestIDHeader, "req-123")
	s.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("response status = %d, want %d", response.Code, http.StatusOK)
	}

	logs := output.String()
	if !strings.Contains(logs, `"__system":"api"`) ||
		!strings.Contains(logs, `"__sys_operation":"GetLiveness"`) ||
		!strings.Contains(logs, `"request_id":"req-123"`) {
		t.Fatalf("API logs lack expected fields: %s", logs)
	}
}

func TestRequestLogsKeepSystemOwnershipAndRequestID(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	root := t.TempDir()
	memoryVault, err := vault.Open(
		context.Background(),
		logger,
		filepath.Join(root, "memoryd.sqlite"),
		filepath.Join(root, "blobs"),
		filepath.Join(root, "uploads"),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := memoryVault.Close(); err != nil {
			t.Error(err)
		}
	})
	memory, err := memoryVault.Put(context.Background(), vault.Import{
		Content:           strings.NewReader("hello"),
		DeclaredMediaType: "",
		Context: vault.ImportContext{
			OriginalFilename:   "note.txt",
			RelativePath:       "",
			FullPath:           "",
			FilesystemCreated:  nil,
			FilesystemModified: nil,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	s, err := New("test", config.Defaults(), logger, memoryVault)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v0/memories/"+memory.ID.String()+"/content",
		nil,
	)
	request.Header.Set(requestIDHeader, "req-123")
	s.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "hello" {
		t.Fatalf("response = %d %q, want 200 hello", response.Code, response.Body.String())
	}

	seenAPI, seenVault := false, false
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		switch record["msg"] {
		case ">> API request starting":
			if record["__system"] != "api" ||
				record["__sys_operation"] != "GetMemoryContent" ||
				record["request_id"] != "req-123" {
				t.Fatalf("API log fields = %#v", record)
			}
			seenAPI = true
		case "Memory Blob opened":
			if record["__system"] != "vault" ||
				record["__sys_operation"] != "OpenContent" ||
				record["request_id"] != "req-123" {
				t.Fatalf("Vault log fields = %#v", record)
			}
			if strings.Count(line, `"__system"`) != 1 ||
				strings.Count(line, `"__sys_operation"`) != 1 {
				t.Fatalf("Vault log has duplicate scope fields: %s", line)
			}
			seenVault = true
		}
	}
	if !seenAPI || !seenVault {
		t.Fatalf("missing API or Vault request log: %s", output.String())
	}
}
