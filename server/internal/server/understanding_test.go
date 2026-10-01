package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/anton-povarov/memoryd/server/internal/api"
	"github.com/anton-povarov/memoryd/server/internal/config"
	"github.com/anton-povarov/memoryd/server/internal/logging"
	"github.com/anton-povarov/memoryd/server/internal/vault"
)

const understandingTestTimeout = 5 * time.Second
const understandingPollInterval = 10 * time.Millisecond

func TestUnsupportedUnderstandingSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	s, memoryVault := openUnderstandingServer(t, root, config.Defaults())
	memory := importUnderstandingMemory(t, s, "opaque.bin", "unsupported fixture bytes")
	detail := understandingMemoryDetail(t, s, memory, "done")

	if detail.Understanding.ActiveRun == nil ||
		len(detail.Understanding.ActiveRun.Artifacts) != 0 ||
		len(detail.Understanding.ActiveRun.Warnings) != 1 ||
		!strings.Contains(detail.Understanding.ActiveRun.Warnings[0], memory.MediaType) {
		t.Fatalf("unsupported outcome = %#v", detail.Understanding)
	}
	runID := detail.Understanding.ActiveRun.ID
	closeUnderstandingServer(t, s, memoryVault)
	s, memoryVault = openUnderstandingServer(t, root, config.Defaults())
	defer closeUnderstandingServer(t, s, memoryVault)
	detail = understandingMemoryDetail(t, s, memory, "done")

	if detail.Understanding.ActiveRun.ID != runID {
		t.Fatalf("restart replaced completed Run: %s", detail.Understanding.ActiveRun.ID)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v0/memories/"+memory.Id.String()+"/content",
		nil,
	)
	s.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Body.String() != "unsupported fixture bytes" {
		t.Fatalf("original Blob = %d %q", response.Code, response.Body.String())
	}
}

const completeUnderstandingResult = `{
  "protocol_version": 2,
  "plugin_version": "fixture-1",
  "artifacts": [
    {
      "content_type": "text/markdown",
      "content": "# Harbour invoice\nTotal AED 125.00\n",
      "provenance": { "method": "embedded-text" },
      "scope": { "page": 1 }
    },
    {
      "content_type": "text/markdown",
      "content": "# Harbour invoice\nTotal AED 125.00\n",
      "provenance": { "method": "model-summary" }
    },
    {
      "content_type": "application/json",
      "content": "{\"total\":\"125.00\",\"currency\":\"AED\"}",
      "provenance": { "method": "field-extraction" }
    }
  ],
  "warnings": ["image coverage limited"]
}`

func TestDerivedContentRetainsIndependentProvenanceAfterRestart(t *testing.T) {
	root := t.TempDir()
	plugin := writeUnderstandingPlugin(
		t,
		root,
		"extract",
		"cat > /dev/null\nprintf '%s\\n' '"+completeUnderstandingResult+"'\n",
	)
	cfg := understandingPluginConfig(plugin)
	s, memoryVault := openUnderstandingServer(t, root, cfg)
	memory := importUnderstandingMemory(t, s, "invoice.pdf", "%PDF-1.7\ninvoice fixture\n%%EOF\n")
	detail := understandingMemoryDetail(t, s, memory, "done")
	run := detail.Understanding.ActiveRun

	if run == nil || run.PluginID != "pdf" || run.PluginVersion != "fixture-1" ||
		run.SourceBlobref != memory.BlobHash || run.AttemptID != detail.Understanding.LatestAttempt.ID {
		t.Fatalf("Run provenance = %#v", run)
	}
	if len(run.Artifacts) != 3 || len(run.Warnings) != 1 {
		t.Fatalf("complete Run = %#v", run)
	}
	text, description, data := run.Artifacts[0], run.Artifacts[1], run.Artifacts[2]

	if text.ID == description.ID || text.BlobHash != description.BlobHash ||
		text.Provenance["method"] != "embedded-text" || description.Provenance["method"] != "model-summary" ||
		text.Scope["page"] != float64(1) {
		t.Fatalf("independent artifact provenance lost: %#v", run.Artifacts)
	}
	if data.ContentType != "application/json" ||
		data.Content != `{"total":"125.00","currency":"AED"}` {
		t.Fatalf("JSON Derived Content = %#v", data)
	}
	closeUnderstandingServer(t, s, memoryVault)
	s, memoryVault = openUnderstandingServer(t, root, cfg)
	defer closeUnderstandingServer(t, s, memoryVault)
	restarted := understandingMemoryDetail(t, s, memory, "done").Understanding.ActiveRun

	if restarted.ID != run.ID || restarted.Artifacts[0].ID != text.ID ||
		restarted.Artifacts[0].Content != text.Content || restarted.Artifacts[1].Provenance["method"] != "model-summary" ||
		restarted.Artifacts[2].Content != data.Content {
		t.Fatalf("restart lost or replaced Derived Content: %#v", restarted)
	}
}

func TestPluginFailuresRequireExplicitRetryAfterRestart(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantError  string
		wantStdout string
		wantStderr string
	}{
		{
			name:       "missing executable",
			body:       "",
			wantError:  "launch plugin",
			wantStdout: "",
			wantStderr: "",
		},
		{
			name:       "nonzero exit",
			body:       "cat > /dev/null\nprintf 'extractor unavailable\\n' >&2\nexit 7\n",
			wantError:  "status 7",
			wantStdout: "",
			wantStderr: "extractor unavailable\n",
		},
		{
			name:       "malformed output",
			body:       "cat > /dev/null\nprintf '{invalid'\n",
			wantError:  "invalid plugin result",
			wantStdout: "{invalid",
			wantStderr: "",
		},
		{
			name:       "missing output",
			body:       "cat > /dev/null\n",
			wantError:  "no result",
			wantStdout: "",
			wantStderr: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			plugin := filepath.Join(root, "extract")

			if test.body != "" {
				writeUnderstandingPlugin(t, root, "extract", test.body)
			}
			cfg := understandingPluginConfig(plugin)
			s, memoryVault := openUnderstandingServer(t, root, cfg)
			memory := importUnderstandingMemory(
				t,
				s,
				"broken.pdf",
				"%PDF-1.7\nfailure fixture\n%%EOF\n",
			)
			failed := understandingMemoryDetail(t, s, memory, "failed").Understanding

			if failed.ActiveRun != nil || failed.LatestAttempt == nil ||
				failed.LatestAttempt.Diagnostics == nil {
				t.Fatalf("failed attempt activated Run or lost diagnostics: %#v", failed)
			}
			diagnostics := failed.LatestAttempt.Diagnostics

			if !strings.Contains(diagnostics.Error, test.wantError) ||
				diagnostics.Stdout != test.wantStdout || diagnostics.Stderr != test.wantStderr {
				t.Fatalf("failure diagnostics = %#v", diagnostics)
			}
			writeUnderstandingPlugin(
				t,
				root,
				"extract",
				"cat > /dev/null\nprintf '%s\\n' '"+completeUnderstandingResult+"'\n",
			)
			unrelated := importUnderstandingMemory(t, s, "note.txt", "unrelated wake fixture")
			understandingMemoryDetail(t, s, unrelated, "done")
			stillFailed := understandingMemoryDetail(t, s, memory, "failed").Understanding

			if stillFailed.LatestAttempt.ID != failed.LatestAttempt.ID {
				t.Fatal("failed plugin retried during same server session")
			}
			closeUnderstandingServer(t, s, memoryVault)
			s, memoryVault = openUnderstandingServer(t, root, cfg)
			defer closeUnderstandingServer(t, s, memoryVault)
			persisted := understandingMemoryDetail(t, s, memory, "failed").Understanding
			if persisted.LatestAttempt.ID != failed.LatestAttempt.ID {
				t.Fatal("restart changed terminal failure")
			}
			requestUnderstandingRefresh(t, s, memory)
			recovered := understandingMemoryDetail(t, s, memory, "done").Understanding

			if recovered.LatestAttempt.ID == failed.LatestAttempt.ID || recovered.ActiveRun == nil {
				t.Fatalf(
					"corrected executable did not receive a new successful attempt: %#v",
					recovered,
				)
			}
		})
	}
}

func TestRefreshMemoryUnderstandingRunsNewAttemptAndPreservesLastRun(t *testing.T) {
	root := t.TempDir()
	plugin := writeUnderstandingPlugin(t, root, "refresh-plugin", `cat > /dev/null
if [ -f "$1/invocations" ]; then
  count=$(cat "$1/invocations")
else
  count=0
fi
count=$((count + 1))
printf '%s\n' "$count" > "$1/invocations"
case "$count" in
  1)
    cat "$1/first.json"
    ;;
  2)
    : > "$1/second-started"
    while [ ! -f "$1/second-release" ]; do sleep 0.01; done
    cat "$1/second.json"
    ;;
  3)
    : > "$1/third-started"
    while [ ! -f "$1/third-release" ]; do sleep 0.01; done
    printf 'fixture failed on rerun\n' >&2
    exit 7
    ;;
  *)
    printf 'unexpected fixture invocation\n' >&2
    exit 8
    ;;
esac
`)
	writeFixture := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeFixture(
		"first.json",
		`{"protocol_version":2,"plugin_version":"fixture-1","artifacts":[{"content_type":"text/plain","content":"initial output"}]}`,
	)
	writeFixture(
		"second.json",
		`{"protocol_version":2,"plugin_version":"fixture-2","artifacts":[{"content_type":"text/plain","content":"manual rerun output"}]}`,
	)

	s, memoryVault := openUnderstandingServer(
		t, root, understandingPluginConfig(plugin, root),
	)
	source := "%PDF-1.7\nmanual refresh source\n%%EOF\n"
	memory := importUnderstandingMemory(t, s, "source.pdf", source)
	initial := understandingMemoryDetail(t, s, memory, "done")
	if initial.Understanding.ActiveRun == nil {
		t.Fatalf("initial completed result = %#v", initial.Understanding)
	}
	initialRun := initial.Understanding.ActiveRun
	initialAttemptID := initialRun.AttemptID

	assertUnderstandingUnavailable(t, s, memoryVault, memory, initial)

	postRefresh := func() *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, httptest.NewRequest(
			http.MethodPost,
			"/api/v0/memories/"+memory.Id.String()+"/rebuild",
			nil,
		))
		return response
	}
	release := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	firstRefresh := postRefresh()
	if firstRefresh.Code != http.StatusAccepted ||
		firstRefresh.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("manual refresh = %d %q", firstRefresh.Code, firstRefresh.Body.String())
	}
	var handle api.RebuildStatus
	if err := json.Unmarshal(firstRefresh.Body.Bytes(), &handle); err != nil {
		t.Fatal(err)
	}
	waitUnderstandingFixture(t, func() bool {
		_, err := os.Stat(filepath.Join(root, "second-started"))
		return err == nil
	})
	pending := understandingMemoryDetail(t, s, memory, "running")
	if pending.Understanding.LatestAttempt == nil ||
		pending.Understanding.LatestAttempt.ID == initialAttemptID ||
		!reflect.DeepEqual(pending.Understanding.ActiveRun, initialRun) {
		t.Fatalf("pending refresh replaced or reused prior result: %#v", pending.Understanding)
	}
	if snapshot := pollUnderstandingAttempt(
		t, s, handle.StatusUrl,
		http.StatusOK,
	); snapshot.Attempt.Status != api.UnderstandingAttemptStateRunning {
		t.Fatalf("pending poll = %#v", snapshot)
	}
	conflict := postRefresh()
	var existing api.RebuildStatus
	if conflict.Code != http.StatusConflict ||
		json.Unmarshal(conflict.Body.Bytes(), &existing) != nil ||
		existing.Attempt.Id != handle.Attempt.Id {
		t.Fatalf("conflicting refresh = %d %s", conflict.Code, conflict.Body.String())
	}

	release("second-release")
	completed := understandingMemoryDetail(t, s, memory, "done")
	if completed.Understanding.ActiveRun == nil ||
		completed.Understanding.ActiveRun.ID == initialRun.ID ||
		len(completed.Understanding.ActiveRun.Artifacts) == 0 ||
		completed.Understanding.ActiveRun.Artifacts[0].Content != "manual rerun output" {
		t.Fatalf("manual refresh did not activate fixture result: %#v", completed.Understanding)
	}
	completedRun := completed.Understanding.ActiveRun
	if snapshot := pollUnderstandingAttempt(
		t, s, handle.StatusUrl,
		http.StatusOK,
	); snapshot.Attempt.Status != api.UnderstandingAttemptStateDone ||
		snapshot.Attempt.Id != handle.Attempt.Id {
		t.Fatalf("terminal poll = %#v", snapshot)
	}

	secondRefresh := postRefresh()
	if secondRefresh.Code != http.StatusAccepted {
		t.Fatalf("second manual refresh = %d %q", secondRefresh.Code, secondRefresh.Body.String())
	}
	pollUnderstandingAttempt(t, s, handle.StatusUrl, http.StatusNotFound)
	waitUnderstandingFixture(t, func() bool {
		_, err := os.Stat(filepath.Join(root, "third-started"))
		return err == nil
	})
	understandingMemoryDetail(t, s, memory, "running")

	release("third-release")
	failed := understandingMemoryDetail(t, s, memory, "failed")
	if failed.Understanding.LatestAttempt == nil ||
		failed.Understanding.LatestAttempt.Diagnostics == nil ||
		!strings.Contains(failed.Understanding.LatestAttempt.Diagnostics.Error, "status 7") ||
		!reflect.DeepEqual(failed.Understanding.ActiveRun, completedRun) {
		t.Fatalf("failed refresh lost prior successful Run: %#v", failed.Understanding)
	}

	contentResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(contentResponse, httptest.NewRequest(
		http.MethodGet,
		"/api/v0/memories/"+memory.Id.String()+"/content",
		nil,
	))
	if contentResponse.Code != http.StatusOK || contentResponse.Body.String() != source {
		t.Fatalf("manual refresh changed original Blob = %d %q",
			contentResponse.Code, contentResponse.Body.String())
	}

	missingResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(missingResponse, httptest.NewRequest(
		http.MethodPost,
		"/api/v0/memories/00000000-0000-0000-0000-000000000000/rebuild",
		nil,
	))
	if missingResponse.Code != http.StatusNotFound {
		t.Fatalf("missing Memory refresh = %d %s",
			missingResponse.Code, missingResponse.Body.String())
	}
}

func TestInterruptedUnderstandingIsLostOnRestart(t *testing.T) {
	root := t.TempDir()
	body := `cat > /dev/null
if ! mkdir "$1/active"; then
  printf 'process limit exceeded\n' >&2
  exit 8
fi
trap 'rmdir "$1/active"' EXIT
echo "$$" >> "$1/starts"
sleep 60 >/dev/null 2>&1 &
child=$!
echo "$child" > "$1/child"
while [ ! -f "$1/release" ]; do sleep 0.01; done
printf '%s\n' '` + completeUnderstandingResult + `'
`
	plugin := writeUnderstandingPlugin(t, root, "gated-extract", body)
	cfg := understandingPluginConfig(plugin, root)
	s, memoryVault := openUnderstandingServer(t, root, cfg)
	first := importUnderstandingMemory(t, s, "first.pdf", "%PDF-1.7\nfirst\n%%EOF\n")
	running := understandingMemoryDetail(t, s, first, "running")
	waitUnderstandingFixture(t, func() bool {
		_, err := os.Stat(filepath.Join(root, "child"))
		return err == nil
	})
	childBytes, err := os.ReadFile(filepath.Join(root, "child"))

	if err != nil {
		t.Fatal(err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(string(childBytes)))

	if err != nil {
		t.Fatal(err)
	}
	second := importUnderstandingMemory(t, s, "second.pdf", "%PDF-1.7\nsecond\n%%EOF\n")
	queued := understandingMemoryDetail(t, s, second, "queued")
	closeUnderstandingServer(t, s, memoryVault)
	waitUnderstandingFixture(t, func() bool {
		return errors.Is(syscall.Kill(childPID, 0), syscall.ESRCH)
	})

	if err := os.Remove(
		filepath.Join(root, "active"),
	); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	s, memoryVault = openUnderstandingServer(t, root, cfg)
	defer closeUnderstandingServer(t, s, memoryVault)
	understandingMemoryDetail(t, s, first, "not_started")
	understandingMemoryDetail(t, s, second, "not_started")
	for _, old := range []string{running.Understanding.LatestAttempt.ID, queued.Understanding.LatestAttempt.ID} {
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet,
			"/api/v0/rebuild-status/"+old, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("old handle = %d", response.Code)
		}
	}
	third := importUnderstandingMemory(t, s, "third.pdf", "%PDF-1.7\nthird\n%%EOF\n")
	understandingMemoryDetail(t, s, third, "running")

	if err := os.WriteFile(filepath.Join(root, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	understandingMemoryDetail(t, s, third, "done")
	understandingMemoryDetail(t, s, first, "not_started")
	understandingMemoryDetail(t, s, second, "not_started")
}

func waitUnderstandingFixture(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(understandingTestTimeout)

	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("plugin fixture did not reach expected state")
		}
		time.Sleep(understandingPollInterval)
	}
}

func TestFailedRunCommitDoesNotExposePartialDerivedContent(t *testing.T) {
	root := t.TempDir()
	plugin := writeUnderstandingPlugin(
		t,
		root,
		"extract",
		"cat > /dev/null\nprintf '%s\\n' '"+completeUnderstandingResult+"'\n",
	)
	cfg := understandingPluginConfig(plugin)
	s, memoryVault := openUnderstandingServer(t, root, cfg)
	database, err := sql.Open("sqlite", filepath.Join(root, "memoryd.sqlite"))

	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	_, err = database.Exec(`CREATE TRIGGER reject_understanding BEFORE INSERT ON understanding_runs
		BEGIN SELECT RAISE(ABORT, 'injected Run commit failure'); END`)

	if err != nil {
		t.Fatal(err)
	}
	source := "%PDF-1.7\ncommit failure fixture\n%%EOF\n"
	memory := importUnderstandingMemory(t, s, "invoice.pdf", source)
	failed := understandingMemoryDetail(t, s, memory, "failed").Understanding

	if failed.ActiveRun != nil || failed.LatestAttempt.Diagnostics == nil ||
		!strings.Contains(failed.LatestAttempt.Diagnostics.Error, "injected Run commit failure") ||
		strings.TrimSpace(failed.LatestAttempt.Diagnostics.Stdout) != completeUnderstandingResult {
		t.Fatalf("failed commit exposed partial Run or lost execution evidence: %#v", failed)
	}
	response := httptest.NewRecorder()
	s.Handler().
		ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v0/memories/"+memory.Id.String()+"/content", nil))

	if response.Code != http.StatusOK || response.Body.String() != source {
		t.Fatalf(
			"commit failure damaged original Blob: %d %q",
			response.Code,
			response.Body.String(),
		)
	}
	if _, err := database.Exec(`DROP TRIGGER reject_understanding`); err != nil {
		t.Fatal(err)
	}
	closeUnderstandingServer(t, s, memoryVault)
	s, memoryVault = openUnderstandingServer(t, root, cfg)
	defer closeUnderstandingServer(t, s, memoryVault)
	understandingMemoryDetail(t, s, memory, "failed")
	requestUnderstandingRefresh(t, s, memory)
	recovered := understandingMemoryDetail(t, s, memory, "done").Understanding

	if recovered.ActiveRun == nil || len(recovered.ActiveRun.Artifacts) != 3 {
		t.Fatalf("commit retry did not activate complete Run: %#v", recovered)
	}
}

func TestRebuildUserNotePersistsAndSnapshots(t *testing.T) {
	root := t.TempDir()
	plugin := writeUnderstandingPlugin(t, root, "note-plugin", `cat > /dev/null
while [ ! -f "$1/release" ]; do sleep 0.01; done
if [ -f "$1/fail" ]; then exit 7; fi
printf '%s\n' '`+completeUnderstandingResult+`'
`)
	cfg := understandingPluginConfig(plugin, root)
	release := func() {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "release"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gate := func() {
		t.Helper()
		if err := os.Remove(filepath.Join(root, "release")); err != nil {
			t.Fatal(err)
		}
	}
	release()
	s, memoryVault := openUnderstandingServer(t, root, cfg)
	memory := importUnderstandingMemory(t, s, "note.pdf", "%PDF-1.7\nnote fixture\n%%EOF\n")
	initial := understandingMemoryDetail(t, s, memory, "done").Understanding
	if initial.UserNote != "" || initial.ActiveRun == nil || initial.ActiveRun.UserNote != "" {
		t.Fatalf("initial notes = %#v", initial)
	}
	post := func(note *string, code int) api.RebuildStatus {
		t.Helper()
		var body io.Reader
		if note != nil {
			encoded, err := json.Marshal(map[string]string{"user_note": *note})
			if err != nil {
				t.Fatal(err)
			}
			body = bytes.NewReader(encoded)
		}
		request := httptest.NewRequest(http.MethodPost,
			"/api/v0/memories/"+memory.Id.String()+"/rebuild", body)
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)
		if response.Code != code {
			t.Fatalf("rebuild = %d %s, want %d", response.Code, response.Body.String(), code)
		}
		var handle api.RebuildStatus
		if err := json.Unmarshal(response.Body.Bytes(), &handle); err != nil {
			t.Fatal(err)
		}
		return handle
	}
	gate()
	note := "  Focus on payment dates.\nСрок оплаты  "
	handle := post(&note, http.StatusAccepted)
	pending := understandingMemoryDetail(t, s, memory, "running").Understanding
	if pending.UserNote != note || pending.ActiveRun.ID != initial.ActiveRun.ID {
		t.Fatalf("pending preference = %#v", pending)
	}
	rejected := "Different rejected note"
	conflict := post(&rejected, http.StatusConflict)
	pending = understandingMemoryDetail(t, s, memory, "running").Understanding
	if conflict.Attempt.Id != handle.Attempt.Id || pending.UserNote != note ||
		pending.LatestAttempt.ID != handle.Attempt.Id.String() {
		t.Fatalf("conflict changed note or handle: %#v, %#v", pending, conflict)
	}
	release()
	success := understandingMemoryDetail(t, s, memory, "done").Understanding
	if success.ActiveRun.ID == initial.ActiveRun.ID || success.ActiveRun.UserNote != note {
		t.Fatalf("successful Run snapshot = %#v", success)
	}
	gate()
	if err := os.WriteFile(filepath.Join(root, "fail"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	nextNote := "Focus on totals"
	post(&nextNote, http.StatusAccepted)
	release()
	failed := understandingMemoryDetail(t, s, memory, "failed").Understanding
	if failed.UserNote != nextNote || !reflect.DeepEqual(failed.ActiveRun, success.ActiveRun) {
		t.Fatalf("failure changed historical Run: %#v", failed)
	}
	closeUnderstandingServer(t, s, memoryVault)
	s, memoryVault = openUnderstandingServer(t, root, cfg)
	defer closeUnderstandingServer(t, s, memoryVault)
	restarted := understandingMemoryDetail(t, s, memory, "failed").Understanding
	if restarted.UserNote != nextNote ||
		!reflect.DeepEqual(restarted.ActiveRun, success.ActiveRun) {
		t.Fatalf("restart lost preference or snapshot: %#v", restarted)
	}
	if err := os.Remove(filepath.Join(root, "fail")); err != nil {
		t.Fatal(err)
	}
	gate()
	post(nil, http.StatusAccepted)
	release()
	reused := understandingMemoryDetail(t, s, memory, "done").Understanding
	if reused.UserNote != nextNote || reused.ActiveRun.UserNote != nextNote ||
		reused.ActiveRun.ID == success.ActiveRun.ID {
		t.Fatalf("bodyless Rebuild did not reuse saved note: %#v", reused)
	}
	gate()
	empty := ""
	post(&empty, http.StatusAccepted)
	release()
	cleared := understandingMemoryDetail(t, s, memory, "done").Understanding
	if cleared.UserNote != "" || cleared.ActiveRun.UserNote != "" ||
		cleared.ActiveRun.ID == reused.ActiveRun.ID {
		t.Fatalf("clear did not publish empty notes: %#v", cleared)
	}
}

func requestUnderstandingRefresh(
	t *testing.T,
	s *Server,
	memory api.MemorySummary,
) {
	t.Helper()
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v0/memories/"+memory.Id.String()+"/rebuild", nil))
	if response.Code != http.StatusAccepted {
		t.Fatalf("refresh = %d %s", response.Code, response.Body.String())
	}
}

func pollUnderstandingAttempt(
	t *testing.T,
	s *Server,
	url string,
	code int,
) api.RebuildStatus {
	t.Helper()
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, url, nil))
	if response.Code != code || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("poll = %d %s", response.Code, response.Body.String())
	}
	var snapshot api.RebuildStatus
	if code == http.StatusOK {
		if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
			t.Fatal(err)
		}
	}
	return snapshot
}

func assertUnderstandingUnavailable(
	t *testing.T,
	s *Server,
	memoryVault *vault.Vault,
	memory api.MemorySummary,
	initial understandingWireDetail,
) {
	t.Helper()
	unavailableHandler := NewHandler("test", s.logger, memoryVault)
	unavailableHTTP := api.HandlerFromMuxWithBaseURL(
		api.NewStrictHandler(unavailableHandler, nil),
		http.NewServeMux(),
		api.ServerUrlLocalMemorydServer,
	)
	response := httptest.NewRecorder()
	unavailableHTTP.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v0/memories/"+memory.Id.String()+"/rebuild", nil))
	var unavailableError api.Error
	if response.Code != http.StatusServiceUnavailable ||
		json.Unmarshal(response.Body.Bytes(), &unavailableError) != nil ||
		unavailableError.Code != "understanding_unavailable" {
		t.Fatalf("worker-unavailable refresh = %d %s", response.Code, response.Body.String())
	}
	unchanged := understandingMemoryDetail(t, s, memory, "done")
	if !reflect.DeepEqual(unchanged.Understanding, initial.Understanding) {
		t.Fatal("worker-unavailable refresh changed Understanding state")
	}
}

func understandingPluginConfig(command ...string) config.Config {
	cfg := config.Defaults()
	cfg.Understanding.Plugins = map[string]config.UnderstandingPluginConfig{
		"pdf": {Command: command, MediaTypes: []string{"application/pdf"}},
	}
	return cfg
}

func writeUnderstandingPlugin(t *testing.T, root, name, body string) string {
	t.Helper()
	path := filepath.Join(root, name)

	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

type understandingWireDetail struct {
	Understanding struct {
		Status        string `json:"status"`
		UserNote      string `json:"user_note"`
		LatestAttempt *struct {
			ID          string `json:"id"`
			Status      string `json:"status"`
			PluginID    string `json:"plugin_id"`
			Diagnostics *struct {
				Error    string `json:"error"`
				Stderr   string `json:"stderr"`
				Stdout   string `json:"stdout"`
				ExitCode *int   `json:"exit_code"`
			} `json:"diagnostics"`
		} `json:"latest_attempt"`
		ActiveRun *struct {
			ID            string   `json:"id"`
			AttemptID     string   `json:"attempt_id"`
			PluginID      string   `json:"plugin_id"`
			PluginVersion string   `json:"plugin_version"`
			SourceBlobref string   `json:"source_blobref"`
			UserNote      string   `json:"user_note"`
			Warnings      []string `json:"warnings"`
			Artifacts     []struct {
				ID          string         `json:"id"`
				BlobHash    string         `json:"blob_hash"`
				ContentType string         `json:"content_type"`
				Content     string         `json:"content"`
				Provenance  map[string]any `json:"provenance"`
				Scope       map[string]any `json:"scope"`
			} `json:"artifacts"`
		} `json:"active_run"`
	} `json:"understanding"`
}

func openUnderstandingServer(t *testing.T, root string, cfg config.Config) (*Server, *vault.Vault) {
	t.Helper()
	logger := logging.New(slog.LevelError, io.Discard)
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
	s, err := New("test", cfg, logger, memoryVault)

	if err != nil {
		_ = memoryVault.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		_ = memoryVault.Close()
	})
	return s, memoryVault
}

func closeUnderstandingServer(t *testing.T, s *Server, memoryVault *vault.Vault) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Error(err)
	}
	if err := memoryVault.Close(); err != nil {
		t.Error(err)
	}
}

func importUnderstandingMemory(
	t *testing.T,
	s *Server,
	filename, content string,
) api.MemorySummary {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", filename)

	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, content); err != nil {
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
		t.Fatalf("import = %d %s", response.Code, response.Body.String())
	}
	var memory api.MemorySummary
	if err := json.Unmarshal(response.Body.Bytes(), &memory); err != nil {
		t.Fatal(err)
	}
	return memory
}

func understandingMemoryDetail(
	t *testing.T,
	s *Server,
	memory api.MemorySummary,
	status string,
) understandingWireDetail {
	t.Helper()
	deadline := time.Now().Add(understandingTestTimeout)
	for {
		request := httptest.NewRequest(http.MethodGet, "/api/v0/memories/"+memory.Id.String(), nil)
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("details = %d %s", response.Code, response.Body.String())
		}
		var detail understandingWireDetail
		if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		if detail.Understanding.Status == "" {
			t.Fatalf("Memory details omit understanding: %s", response.Body.String())
		}
		if detail.Understanding.Status == status {
			return detail
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"understanding status = %s, want %s; response %s",
				detail.Understanding.Status,
				status,
				response.Body.String(),
			)
		}
		time.Sleep(understandingPollInterval)
	}
}
