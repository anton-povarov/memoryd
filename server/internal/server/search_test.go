package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/anton-povarov/memoryd/server/internal/api"
	"github.com/anton-povarov/memoryd/server/internal/config"
	"github.com/anton-povarov/memoryd/server/internal/vault"
)

func TestSearchMemoriesHTTPWholeVaultAndMetadata(t *testing.T) {
	s, memoryVault := openUnderstandingServer(t, t.TempDir(), config.Defaults())
	targetBody := "Anton passport issued here.\n<script>window.searchSmokeExecuted=true</script>"
	target := importUnderstandingMemory(t, s, "opaque.md", targetBody)
	importUnderstandingMemory(t, s, "passport-without-person.txt", "A passport without a person.")

	for index := range 51 {
		_, err := memoryVault.Put(context.Background(), vault.Import{
			Content:       strings.NewReader(fmt.Sprintf("distinct noise corpus item %d", index)),
			MediaTypeHint: "text/plain",
			Context: vault.ImportContext{
				OriginalFilename: fmt.Sprintf("noise-%02d.txt", index),
			},
		})
		if err != nil {
			t.Fatalf("seed noise Memory %d: %v", index, err)
		}
	}

	browse := httptest.NewRecorder()
	s.Handler().
		ServeHTTP(browse, httptest.NewRequest(http.MethodGet, "/api/v0/memories?limit=50", nil))
	if browse.Code != http.StatusOK {
		t.Fatalf("browse = %d %s", browse.Code, browse.Body.String())
	}
	var browsed api.MemoryPage
	if err := json.Unmarshal(browse.Body.Bytes(), &browsed); err != nil {
		t.Fatal(err)
	}
	for _, item := range browsed.Items {
		if item.Id == target.Id {
			t.Fatalf(
				"old target unexpectedly appears in first browse page of %d",
				len(browsed.Items),
			)
		}
	}

	response, page := searchHTTP(t, s, "/api/v0/memories/search?query=passports%20Anton&limit=1")
	assertSearchCacheControl(t, response)
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Memory.Id != target.Id {
		t.Fatalf("mandatory stemmed search = %#v", page)
	}
	if page.QueryPlan.Query != "passports Anton" || len(page.QueryPlan.Terms) != 2 ||
		page.QueryPlan.Terms[0] != "passports" || page.QueryPlan.Terms[1] != "anton" {
		t.Fatalf("query plan = %#v", page.QueryPlan)
	}
	var excerpt strings.Builder
	matched := false
	for _, part := range page.Items[0].Excerpt {
		if strings.ContainsAny(part.Text, "\x01\x02") {
			t.Fatalf("excerpt exposed snippet markers: %#v", part)
		}
		matched = matched || part.Match
		excerpt.WriteString(part.Text)
	}
	if !matched ||
		!strings.Contains(excerpt.String(), "<script>window.searchSmokeExecuted=true</script>") {
		t.Fatalf("safe body excerpt = %#v (%q)", page.Items[0].Excerpt, excerpt.String())
	}
	if strings.Contains(response.Body.String(), "<script>") {
		t.Fatalf("JSON response emitted unescaped script markup: %s", response.Body.String())
	}

}

func TestSearchMemoriesHTTPMetadataAndNotes(t *testing.T) {
	s, _ := openUnderstandingServer(t, t.TempDir(), config.Defaults())
	russian := importUnderstandingMemory(t, s, "russian.txt", "Русское слово игры здесь.")
	_, page := searchHTTP(t, s, "/api/v0/memories/search?query="+url.QueryEscape("игры"))
	if page.Total != 1 || page.Items[0].Memory.Id != russian.Id {
		t.Fatalf("Russian search = %#v", page)
	}

	filename := importUnderstandingMemory(t, s, "TitleNeedle.txt", "ordinary original body")
	_, page = searchHTTP(t, s, "/api/v0/memories/search?query=titleneedle")
	if page.Total != 1 || page.Items[0].Memory.Id != filename.Id ||
		len(page.Items[0].Excerpt) != 0 {
		t.Fatalf("filename-only search fabricated excerpt: %#v", page)
	}

	pathMemory := importSearchMemoryWithContext(
		t,
		s,
		"object.txt",
		"ordinary path fixture",
		"archive/PathNeedle/object.txt",
	)
	_, page = searchHTTP(t, s, "/api/v0/memories/search?query=pathneedle")
	if page.Total != 1 || page.Items[0].Memory.Id != pathMemory.Id ||
		len(page.Items[0].Excerpt) != 0 {
		t.Fatalf("path-only search fabricated excerpt: %#v", page)
	}

	opaque := importUnderstandingMemory(t, s, "package.bin", "\x00opaque binary bytes\xff")
	understandingMemoryDetail(t, s, opaque, "done")
	setSearchNote(t, s, opaque, "Games Civilization2 Nightingale")
	_, page = searchHTTP(t, s, "/api/v0/memories/search?query=nightingale")
	if page.Total != 1 || page.Items[0].Memory.Id != opaque.Id {
		t.Fatalf("saved opaque-Memory note search = %#v", page)
	}

}

func TestSearchMemoriesHTTPFreshnessHeadersAndInvalidParameters(t *testing.T) {
	s, _ := openUnderstandingServer(t, t.TempDir(), config.Defaults())
	first := importUnderstandingMemory(t, s, "first.txt", "freshnessmarker source one")
	searchURL := "/api/v0/memories/search?query=freshnessmarker"
	response, page := searchHTTP(t, s, searchURL)
	assertSearchCacheControl(t, response)
	if page.Total != 1 || page.Items[0].Memory.Id != first.Id {
		t.Fatalf("first freshness response = %#v", page)
	}

	second := importUnderstandingMemory(t, s, "second.txt", "freshnessmarker source two")
	response, page = searchHTTP(t, s, searchURL)
	assertSearchCacheControl(t, response)
	if page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("same URL after import = %#v", page)
	}

	deleted := httptest.NewRecorder()
	s.Handler().ServeHTTP(deleted, httptest.NewRequest(http.MethodDelete,
		"/api/v0/memories/"+second.Id.String(), nil))
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete second = %d %s", deleted.Code, deleted.Body.String())
	}
	response, page = searchHTTP(t, s, searchURL)
	assertSearchCacheControl(t, response)
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Memory.Id != first.Id {
		t.Fatalf("same URL after delete = %#v", page)
	}

	response, page = searchHTTP(t, s, "/api/v0/memories/search?query=notfoundmarker")
	assertSearchCacheControl(t, response)
	if page.Total != 0 || page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("empty result = %#v", page)
	}

	for _, test := range []struct {
		path, message string
	}{
		{"/api/v0/memories/search?query=", "search query must contain at least one word"},
		{"/api/v0/memories/search?query=x&limit=0", "search limit must be positive"},
	} {
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d %s", test.path, response.Code, response.Body.String())
		}
		assertSearchCacheControl(t, response)
		var body api.Error
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode typed validation response: %v (%s)", err, response.Body.String())
		}
		if body.Code != "invalid_search" || body.Message != test.message {
			t.Fatalf("typed validation body = %#v", body)
		}
	}

	for _, path := range []string{
		"/api/v0/memories/search",
		"/api/v0/memories/search?query=x&limit=not-an-integer",
		"/api/v0/memories/search?query=x&limit=9223372036854775808",
	} {
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("malformed request %s = %d %s", path, response.Code, response.Body.String())
		}
	}
}

func searchHTTP(t *testing.T, s *Server, path string) (*httptest.ResponseRecorder, api.SearchPage) {
	t.Helper()
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("search %s = %d %s", path, response.Code, response.Body.String())
	}
	var page api.SearchPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode search response: %v (%s)", err, response.Body.String())
	}
	return response, page
}

func assertSearchCacheControl(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func importSearchMemoryWithContext(
	t *testing.T, s *Server, filename, content, relativePath string,
) api.MemorySummary {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, content); err != nil {
		t.Fatal(err)
	}
	encodedContext, err := json.Marshal(api.ImportContextInput{RelativePath: &relativePath})
	if err != nil {
		t.Fatal(err)
	}
	if err := form.WriteField("import_context", string(encodedContext)); err != nil {
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
		t.Fatalf("context import = %d %s", response.Code, response.Body.String())
	}
	var memory api.MemorySummary
	if err := json.Unmarshal(response.Body.Bytes(), &memory); err != nil {
		t.Fatal(err)
	}
	return memory
}

func setSearchNote(t *testing.T, s *Server, memory api.MemorySummary, note string) {
	t.Helper()
	body, err := json.Marshal(api.RebuildRequest{UserNote: &note})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost,
		"/api/v0/memories/"+memory.Id.String()+"/rebuild", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("save search note = %d %s", response.Code, response.Body.String())
	}
}
