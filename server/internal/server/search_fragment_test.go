package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anton-povarov/memoryd/server/internal/api"
	"github.com/anton-povarov/memoryd/server/internal/config"
)

func TestSearchMemoriesHTTPFragmentsRetainFreshnessAndSafeExcerpts(t *testing.T) {
	s, _ := openUnderstandingServer(t, t.TempDir(), config.Defaults())
	bodyURL := "/api/v0/memories/search?query=anton"
	noteURL := "/api/v0/memories/search?query=iv2"

	response, page := searchHTTP(t, s, bodyURL)
	assertSearchCacheControl(t, response)
	if page.Total != 0 || page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("initial body fragment response = %#v", page)
	}

	memory := importUnderstandingMemory(
		t,
		s,
		"opaque.txt",
		"microAntonrecord <script>window.fragmentSmokeExecuted=true</script>",
	)
	response, page = searchHTTP(t, s, bodyURL)
	assertSearchCacheControl(t, response)
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Memory.Id != memory.Id {
		t.Fatalf("body fragment response after import = %#v", page)
	}
	if matchedSearchExcerptText(page.Items[0].Excerpt) != "Anton" {
		t.Fatalf("body fragment excerpt lost original case: %#v", page.Items[0].Excerpt)
	}
	if strings.Contains(response.Body.String(), "<script>") ||
		strings.ContainsAny(response.Body.String(), "\x01\x02") {
		t.Fatalf("search response exposed HTML or snippet markers: %s", response.Body.String())
	}

	response, page = searchHTTP(t, s, noteURL)
	assertSearchCacheControl(t, response)
	if page.Total != 0 || page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("initial note fragment response = %#v", page)
	}
	understandingMemoryDetail(t, s, memory, "done")
	setSearchNote(t, s, memory, "private microCIV2note")
	response, page = searchHTTP(t, s, noteURL)
	assertSearchCacheControl(t, response)
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Memory.Id != memory.Id ||
		matchedSearchExcerptText(page.Items[0].Excerpt) != "IV2" {
		t.Fatalf("saved-note fragment response = %#v", page)
	}

	understandingMemoryDetail(t, s, memory, "done")
	setSearchNote(t, s, memory, "replacement IV2 note")
	response, page = searchHTTP(t, s, noteURL)
	assertSearchCacheControl(t, response)
	if page.Total != 1 || len(page.Items) != 1 ||
		matchedSearchExcerptText(page.Items[0].Excerpt) != "IV2" {
		t.Fatalf("updated-note fragment response = %#v", page)
	}
	response, page = searchHTTP(t, s, "/api/v0/memories/search?query=civ2")
	assertSearchCacheControl(t, response)
	if page.Total != 0 {
		t.Fatalf("replaced note fragment remained searchable: %#v", page)
	}

	understandingMemoryDetail(t, s, memory, "done")
	setSearchNote(t, s, memory, "")
	response, page = searchHTTP(t, s, noteURL)
	assertSearchCacheControl(t, response)
	if page.Total != 0 || page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("cleared-note fragment response = %#v", page)
	}
	understandingMemoryDetail(t, s, memory, "done")
	setSearchNote(t, s, memory, "private microCIV2note")
	response, page = searchHTTP(t, s, noteURL)
	assertSearchCacheControl(t, response)
	if page.Total != 1 {
		t.Fatalf("note fragment before deletion = %#v", page)
	}

	deleted := httptest.NewRecorder()
	s.Handler().ServeHTTP(deleted, httptest.NewRequest(
		http.MethodDelete,
		"/api/v0/memories/"+memory.Id.String(),
		nil,
	))
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete Memory = %d %s", deleted.Code, deleted.Body.String())
	}
	for _, searchURL := range []string{bodyURL, noteURL} {
		response, page = searchHTTP(t, s, searchURL)
		assertSearchCacheControl(t, response)
		if page.Total != 0 || page.Items == nil || len(page.Items) != 0 {
			t.Errorf("same URL after deletion %s = %#v", searchURL, page)
		}
	}
}

func matchedSearchExcerptText(parts []api.SearchExcerptPart) string {
	var matched strings.Builder
	for _, part := range parts {
		if strings.ContainsAny(part.Text, "\x01\x02") {
			return "marker-leak"
		}
		if part.Match {
			matched.WriteString(part.Text)
		}
	}
	return matched.String()
}
