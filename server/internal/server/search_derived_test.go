package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anton-povarov/memoryd/server/internal/api"
)

func TestSearchMemoriesHTTPUsesOnlyActiveDerivedContent(t *testing.T) {
	root := t.TempDir()
	writeSearchDerivedFixture(
		t,
		root,
		"first.json",
		`{"protocol_version":2,"plugin_version":"fixture-1","artifacts":[{"content_type":"text/markdown","content":"# Description\nPassport overview issued to Anton.\n"},{"content_type":"application/json","content":"{\"facts\":[[\"document_number\",\"ZX-481\",\"passport number\"]],\"schema\":\"memoryd.document_data.v2\"}","provenance":{"schema":"memoryd.document_data.v2"}}]}`,
	)
	writeSearchDerivedFixture(
		t,
		root,
		"second.json",
		`{"protocol_version":2,"plugin_version":"fixture-2","artifacts":[{"content_type":"text/markdown","content":"# Description\nInvoice summary prepared for Blair.\n"},{"content_type":"application/json","content":"{\"currency\":\"AED\",\"total\":125}"},{"content_type":"application/json","content":"unsupportedmalformed literal"}]}`,
	)
	plugin := writeUnderstandingPlugin(t, root, "search-derived-plugin", `cat > /dev/null
if [ -f "$1/invocations" ]; then count=$(cat "$1/invocations"); else count=0; fi
count=$((count + 1))
printf '%s\n' "$count" > "$1/invocations"
case "$count" in
  1) cat "$1/first.json" ;;
  2) cat "$1/second.json" ;;
  3) printf 'fixture-log-only phrase\n' >&2; exit 7 ;;
  *) printf 'unexpected invocation\n' >&2; exit 8 ;;
esac
`)
	s, _ := openUnderstandingServer(t, root, understandingPluginConfig(plugin, root))
	memory := importUnderstandingMemory(
		t,
		s,
		"opaque-source.pdf",
		"%PDF-1.7\nsource contains no search terms\n%%EOF\n",
	)
	initial := understandingMemoryDetail(t, s, memory, "done")
	if initial.Understanding.ActiveRun == nil {
		t.Fatalf("initial active Run missing: %#v", initial.Understanding)
	}

	_, page := searchHTTP(t, s, "/api/v0/memories/search?query=passport%20Anton")
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Memory.Id != memory.Id {
		t.Fatalf("description search = %#v", page)
	}
	matched := false
	for _, part := range page.Items[0].Excerpt {
		if strings.ContainsAny(part.Text, "\x01\x02") {
			t.Fatalf("derived excerpt exposed match marker: %#v", part)
		}
		matched = matched || part.Match && strings.Contains(strings.ToLower(part.Text), "passport")
	}
	if !matched {
		t.Fatalf("description-only match lacks marked excerpt: %#v", page.Items[0].Excerpt)
	}
	_, page = searchHTTP(t, s, "/api/v0/memories/search?query=ZX-481")
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Memory.Id != memory.Id {
		t.Fatalf("structured value search = %#v", page)
	}

	second := requestSearchDerivedRebuild(t, s, memory)
	waitSearchDerivedAttempt(t, s, second.StatusUrl, api.UnderstandingAttemptStateDone)
	completed := understandingMemoryDetail(t, s, memory, "done")
	if completed.Understanding.ActiveRun == nil ||
		completed.Understanding.ActiveRun.ID == initial.Understanding.ActiveRun.ID {
		t.Fatalf("successful Rebuild did not replace active Run: %#v", completed.Understanding)
	}
	_, page = searchHTTP(t, s, "/api/v0/memories/search?query=passport")
	if page.Total != 0 {
		t.Fatalf("historical artifact remained searchable: %#v", page.Items)
	}
	_, page = searchHTTP(t, s, "/api/v0/memories/search?query=invoice%20Blair")
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Memory.Id != memory.Id {
		t.Fatalf("replacement description search = %#v", page)
	}
	_, page = searchHTTP(t, s, "/api/v0/memories/search?query=125%20AED")
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("replacement structured-value search = %#v", page)
	}
	_, page = searchHTTP(t, s, "/api/v0/memories/search?query=unsupportedmalformed")
	if page.Total != 0 {
		t.Fatalf("unparseable JSON was indexed as structured content: %#v", page)
	}

	failed := requestSearchDerivedRebuild(t, s, memory)
	waitSearchDerivedAttempt(t, s, failed.StatusUrl, api.UnderstandingAttemptStateFailed)
	failedDetail := understandingMemoryDetail(t, s, memory, "failed")
	if failedDetail.Understanding.ActiveRun == nil ||
		failedDetail.Understanding.ActiveRun.ID != completed.Understanding.ActiveRun.ID {
		t.Fatalf("failed Rebuild replaced active Run: %#v", failedDetail.Understanding)
	}
	_, page = searchHTTP(t, s, "/api/v0/memories/search?query=invoice%20Blair")
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Memory.Id != memory.Id {
		t.Fatalf("failed Rebuild lost previous active text: %#v", page.Items)
	}
	_, page = searchHTTP(t, s, "/api/v0/memories/search?query=fixture-log-only")
	if page.Total != 0 {
		t.Fatalf("processing log became searchable: %#v", page.Items)
	}
}

func writeSearchDerivedFixture(t *testing.T, root, name, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func requestSearchDerivedRebuild(
	t *testing.T,
	s *Server,
	memory api.MemorySummary,
) api.RebuildStatus {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost,
		"/api/v0/memories/"+memory.Id.String()+"/rebuild", nil)
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("request Rebuild = %d %s", response.Code, response.Body.String())
	}
	var handle api.RebuildStatus
	if err := json.Unmarshal(response.Body.Bytes(), &handle); err != nil {
		t.Fatal(err)
	}
	return handle
}

func waitSearchDerivedAttempt(
	t *testing.T,
	s *Server,
	statusURL string,
	want api.UnderstandingAttemptState,
) {
	t.Helper()
	waitUnderstandingFixture(t, func() bool {
		return pollUnderstandingAttempt(t, s, statusURL, http.StatusOK).Attempt.Status == want
	})
}
