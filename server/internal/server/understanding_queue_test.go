package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anton-povarov/memoryd/server/internal/config"
	"github.com/anton-povarov/memoryd/server/internal/logging"
	"github.com/anton-povarov/memoryd/server/internal/vault"
	"github.com/google/uuid"
)

const queueTestPluginResult = `{"protocol_version":2,"plugin_version":"queue-test","artifacts":[{"content_type":"text/markdown","content":"queue fixture","provenance":{"method":"test"}}],"warnings":[]}`

func TestUnderstandingQueueFIFOForgetRetentionAndRequestCorrelation(t *testing.T) {
	root := t.TempDir()
	startDir := filepath.Join(root, "started")
	if err := os.Mkdir(startDir, 0o700); err != nil {
		t.Fatal(err)
	}
	orderPath := filepath.Join(root, "order")
	releasePath := filepath.Join(root, "release")
	logDir := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(logDir, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	plugin := queueTestPlugin(t, root, startDir, orderPath, releasePath)
	var logs strings.Builder
	logger := logging.NewFromSlog(slog.New(slog.NewJSONHandler(&logs, nil)))
	memoryVault := queueTestVault(t, root, logger)
	cfg := config.Defaults().Understanding
	cfg.MaxConcurrent = 1
	cfg.Plugins = map[string]config.UnderstandingPluginConfig{
		"queue-test": {
			Command:    plugin,
			MediaTypes: []string{"application/pdf"},
			LogDir:     logDir,
		},
	}
	worker, err := newUnderstandingWorker(memoryVault, cfg, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		worker.Close()
		_ = memoryVault.Close()
	})

	first := queueTestMemory(t, memoryVault, "a.pdf")
	second := queueTestMemory(t, memoryVault, "b.pdf")
	forgotten := queueTestMemory(t, memoryVault, "c.pdf")
	requestCtx, cancelRequest := context.WithCancel(
		logging.ContextWithRequestID(context.Background(), "request-a"),
	)
	firstAttempt, accepted, err := worker.Enqueue(requestCtx, first.ID)
	if err != nil || !accepted {
		t.Fatalf("Enqueue(first) = %v, %t", err, accepted)
	}
	cancelRequest()
	waitQueueFile(t, filepath.Join(startDir, "a.pdf"))
	waitQueueStatus(t, worker, first.ID, "running")

	duplicateCtx := logging.ContextWithRequestID(context.Background(), "request-duplicate")
	duplicate, accepted, err := worker.Enqueue(duplicateCtx, first.ID)
	if err != nil || accepted || duplicate.ID != firstAttempt.ID || duplicate.Status != "running" {
		t.Fatalf("duplicate Enqueue = %#v, %t, %v", duplicate, accepted, err)
	}
	secondAttempt, accepted, err := worker.Enqueue(
		logging.ContextWithRequestID(context.Background(), "request-b"), second.ID,
	)
	if err != nil || !accepted || secondAttempt.Status != "queued" {
		t.Fatalf("Enqueue(second) = %#v, %t, %v", secondAttempt, accepted, err)
	}
	queuedDuplicate, accepted, err := worker.Enqueue(context.Background(), second.ID)
	if err != nil || accepted || queuedDuplicate.ID != secondAttempt.ID ||
		queuedDuplicate.Status != "queued" {
		t.Fatalf("queued duplicate Enqueue = %#v, %t, %v", queuedDuplicate, accepted, err)
	}
	forgottenAttempt, accepted, err := worker.Enqueue(context.Background(), forgotten.ID)
	if err != nil || !accepted {
		t.Fatalf("Enqueue(forgotten) = %v, %t", err, accepted)
	}
	worker.Forget(forgotten.ID)
	if _, ok := worker.Snapshot(forgottenAttempt.ID); ok {
		t.Fatal("Forget retained queued handle")
	}
	if _, err := os.Stat(filepath.Join(startDir, "b.pdf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second plugin started before first released: %v", err)
	}

	if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitQueueStatus(t, worker, first.ID, "done")
	waitQueueStatus(t, worker, second.ID, "failed")
	assertQueueSnapshotIsolation(t, worker, second.ID, secondAttempt.ID)
	if old, ok := worker.Snapshot(firstAttempt.ID); !ok || old.Status != "done" {
		t.Fatalf("successful handle not retained: %#v, %t", old, ok)
	}

	replacement, accepted, err := worker.Enqueue(
		logging.ContextWithRequestID(context.Background(), "request-a-retry"), first.ID,
	)
	if err != nil || !accepted || replacement.ID == firstAttempt.ID {
		t.Fatalf("terminal retry admission = %#v, %t, %v", replacement, accepted, err)
	}
	if _, ok := worker.Snapshot(firstAttempt.ID); ok {
		t.Fatal("superseded handle still resolves")
	}
	waitQueueStatus(t, worker, first.ID, "done")
	worker.Close()

	assertQueueRequestLogs(t, logs.String(), map[uuid.UUID]string{
		firstAttempt.ID:  "request-a",
		secondAttempt.ID: "request-b",
		replacement.ID:   "request-a-retry",
	})
	if got := queueTestLines(t, orderPath); strings.Join(got, ",") != "a.pdf,b.pdf,a.pdf" {
		t.Fatalf("plugin FIFO order = %v", got)
	}
	if _, err := os.Stat(filepath.Join(startDir, "c.pdf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("forgotten job executed: %v", err)
	}
}

func assertQueueSnapshotIsolation(
	t *testing.T,
	worker *understandingWorker,
	memoryID, attemptID uuid.UUID,
) {
	t.Helper()
	retained, ok := worker.Snapshot(attemptID)
	if !ok || retained.Status != "failed" || retained.Diagnostics == nil ||
		retained.Diagnostics.ExitCode == nil || *retained.Diagnostics.ExitCode != 7 {
		t.Fatalf("terminal failure snapshot = %#v, %t", retained, ok)
	}
	wantError, wantExitCode := retained.Diagnostics.Error, *retained.Diagnostics.ExitCode
	retained.StartedAt = nil
	retained.CompletedAt = nil
	retained.Diagnostics.Error = "mutated"
	*retained.Diagnostics.ExitCode = 99
	latest, _ := worker.Latest(memoryID)
	if latest.Status != "failed" || latest.StartedAt == nil || latest.CompletedAt == nil ||
		latest.Diagnostics == nil || latest.Diagnostics.Error != wantError ||
		latest.Diagnostics.ExitCode == nil || *latest.Diagnostics.ExitCode != wantExitCode {
		t.Fatalf("mutating terminal snapshot changed retained state: %#v", latest)
	}
}

func TestUnderstandingQueueConcurrentAdmissionLimitAndShutdown(t *testing.T) {
	root := t.TempDir()
	startDir := filepath.Join(root, "started")
	if err := os.Mkdir(startDir, 0o700); err != nil {
		t.Fatal(err)
	}
	orderPath := filepath.Join(root, "order")
	releasePath := filepath.Join(root, "release")
	plugin := queueTestPlugin(t, root, startDir, orderPath, releasePath)
	logger := logging.NewFromSlog(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	memoryVault := queueTestVault(t, root, logger)
	cfg := config.Defaults().Understanding
	cfg.MaxConcurrent = 2
	cfg.Plugins = map[string]config.UnderstandingPluginConfig{
		"queue-test": {Command: plugin, MediaTypes: []string{"application/pdf"}},
	}
	worker, err := newUnderstandingWorker(memoryVault, cfg, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		worker.Close()
		_ = memoryVault.Close()
	}()

	memories := []vault.Memory{
		queueTestMemory(t, memoryVault, "parallel-a.pdf"),
		queueTestMemory(t, memoryVault, "parallel-b.pdf"),
		queueTestMemory(t, memoryVault, "parallel-c.pdf"),
	}
	for _, memory := range memories {
		if _, accepted, err := worker.Enqueue(
			context.Background(),
			memory.ID,
		); err != nil ||
			!accepted {
			t.Fatalf("Enqueue(%s) = %t, %v", memory.ID, accepted, err)
		}
	}
	waitQueueFile(t, filepath.Join(startDir, "parallel-a.pdf"))
	waitQueueFile(t, filepath.Join(startDir, "parallel-b.pdf"))
	if _, err := os.Stat(
		filepath.Join(startDir, "parallel-c.pdf"),
	); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("worker exceeded configured concurrency: %v", err)
	}

	var duplicates sync.WaitGroup
	results := make(chan error, 16)
	for index := 0; index < 16; index++ {
		duplicates.Add(1)
		go func() {
			defer duplicates.Done()
			attempt, accepted, err := worker.Enqueue(context.Background(), memories[0].ID)
			if err != nil || accepted || attempt.Status != "running" {
				results <- errors.New("concurrent duplicate admission was not rejected")
			}
		}()
	}
	duplicates.Wait()
	close(results)
	for err := range results {
		t.Error(err)
	}

	if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, memory := range memories {
		waitQueueStatus(t, worker, memory.ID, "done")
	}
	order := queueTestLines(t, orderPath)
	if len(order) != 3 || order[2] != "parallel-c.pdf" {
		t.Fatalf("queued job did not run after both slots freed: %v", order)
	}

	shutdownRoot := filepath.Join(root, "shutdown")
	shutdownStart := filepath.Join(shutdownRoot, "started")
	if err := os.MkdirAll(shutdownStart, 0o700); err != nil {
		t.Fatal(err)
	}
	shutdownOrder := filepath.Join(shutdownRoot, "order")
	shutdownRelease := filepath.Join(shutdownRoot, "release")
	shutdownPlugin := queueTestPlugin(
		t, shutdownRoot, shutdownStart, shutdownOrder, shutdownRelease,
	)
	shutdownCfg := cfg
	shutdownCfg.MaxConcurrent = 1
	shutdownCfg.Plugins = map[string]config.UnderstandingPluginConfig{
		"queue-test": {Command: shutdownPlugin, MediaTypes: []string{"application/pdf"}},
	}
	shutdownWorker, err := newUnderstandingWorker(memoryVault, shutdownCfg, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownWorker.Close()
	forgottenRunning, accepted, err := shutdownWorker.Enqueue(context.Background(), memories[0].ID)
	if err != nil || !accepted {
		t.Fatalf("Enqueue(forget-running job) = %t, %v", accepted, err)
	}
	waitQueueFile(t, filepath.Join(shutdownStart, "parallel-a.pdf"))
	shutdownWorker.Forget(memories[0].ID)
	if _, ok := shutdownWorker.Latest(memories[0].ID); ok {
		t.Fatal("Forget retained running handle")
	}
	if err := os.WriteFile(shutdownRelease, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitQueueRun(t, memoryVault, memories[0].ID, forgottenRunning.ID)
	if _, ok := shutdownWorker.Latest(memories[0].ID); ok {
		t.Fatal("forgotten execution republished its handle")
	}

	if err := os.Remove(shutdownRelease); err != nil {
		t.Fatal(err)
	}
	if _, accepted, err := shutdownWorker.Enqueue(
		context.Background(),
		memories[1].ID,
	); err != nil ||
		!accepted {
		t.Fatalf("Enqueue(shutdown job) = %t, %v", accepted, err)
	}
	waitQueueFile(t, filepath.Join(shutdownStart, "parallel-b.pdf"))
	shutdownWorker.Close()
	if _, ok := shutdownWorker.Latest(memories[1].ID); ok {
		t.Fatal("shutdown retained polling state")
	}
	if _, _, err := shutdownWorker.Enqueue(
		context.Background(),
		memories[1].ID,
	); !errors.Is(
		err,
		errUnderstandingUnavailable,
	) {
		t.Fatalf("Enqueue after shutdown error = %v", err)
	}
}

func queueTestVault(t *testing.T, root string, logger *logging.Logger) *vault.Vault {
	t.Helper()
	memoryVault, err := vault.Open(context.Background(), logger,
		filepath.Join(root, "memoryd.sqlite"), filepath.Join(root, "blobs"),
		filepath.Join(root, "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	return memoryVault
}

func queueTestMemory(t *testing.T, memoryVault *vault.Vault, name string) vault.Memory {
	t.Helper()
	memory, err := memoryVault.Put(context.Background(), vault.Import{
		Content:       strings.NewReader("%PDF-1.7\n" + name + "\n%%EOF\n"),
		MediaTypeHint: "application/pdf",
		Context:       vault.ImportContext{OriginalFilename: name},
	})
	if err != nil {
		t.Fatal(err)
	}
	return memory
}

func queueTestPlugin(t *testing.T, root, startDir, orderPath, releasePath string) []string {
	t.Helper()
	script := `request=$(cat)
name=$(printf '%s' "$request" | grep -o '"original_filename":"[^"]*"' | cut -d '"' -f 4)
printf '%s\n' "$name" >> "$1"
: > "$2/$name"
printf 'MEMORYD_PROGRESS {"phase":"model_started","turn":1}\n' >&2
while [ ! -f "$3" ]; do sleep 0.01; done
if [ "$name" = "b.pdf" ]; then printf 'fixture failure\n' >&2; exit 7; fi
printf '%s\n' '` + queueTestPluginResult + `'
`
	path := filepath.Join(root, "queue-plugin.sh")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	return []string{"/bin/sh", path, orderPath, startDir, releasePath}
}

func waitQueueFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func waitQueueStatus(
	t *testing.T,
	worker *understandingWorker,
	memoryID uuid.UUID,
	status string,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if attempt, ok := worker.Latest(memoryID); ok && attempt.Status == status {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	attempt, ok := worker.Latest(memoryID)
	t.Fatalf("timed out waiting for %s status: %#v, exists=%t", status, attempt, ok)
}
func waitQueueRun(
	t *testing.T,
	memoryVault *vault.Vault,
	memoryID, attemptID uuid.UUID,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		details, err := memoryVault.Understanding(context.Background(), memoryID)
		if err == nil && details.ActiveRun != nil && details.ActiveRun.AttemptID == attemptID {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for active Run from attempt %s", attemptID)
}

func queueTestLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

func assertQueueRequestLogs(t *testing.T, data string, expected map[uuid.UUID]string) {
	t.Helper()
	phases := make(map[uuid.UUID]bool)
	rawLogErrors := make(map[uuid.UUID]bool)
	for _, line := range strings.Split(strings.TrimSpace(data), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode worker log %q: %v", line, err)
		}
		attemptText, ok := record["attempt_id"].(string)
		if !ok {
			continue
		}
		attemptID, err := uuid.Parse(attemptText)
		if err != nil {
			continue
		}
		wantRequestID, ok := expected[attemptID]
		if !ok {
			continue
		}
		if record["request_id"] != wantRequestID {
			t.Fatalf("attempt %s log request_id = %v, want %s (%s)",
				attemptID, record["request_id"], wantRequestID, record["msg"])
		}
		switch record["msg"] {
		case "Understanding phase model_started":
			phases[attemptID] = true
		case "create understanding log directory failed":
			rawLogErrors[attemptID] = true
		}
	}
	for attemptID := range expected {
		if !phases[attemptID] || !rawLogErrors[attemptID] {
			t.Errorf("attempt %s missing correlated phase/raw-log error: phase=%t raw_log=%t",
				attemptID, phases[attemptID], rawLogErrors[attemptID])
		}
	}
}
