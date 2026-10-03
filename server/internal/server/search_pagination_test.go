package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/anton-povarov/memoryd/server/internal/api"
	"github.com/anton-povarov/memoryd/server/internal/config"
)

func TestSearchMemoriesHTTPContinuationReturnsCompleteOrderedPages(t *testing.T) {
	s, _ := openUnderstandingServer(t, t.TempDir(), config.Defaults())
	for _, memory := range []struct{ filename, body string }{
		{"precise-a.txt", "paginationmarker boundarytoken"},
		{"precise-b.txt", "boundarytoken paginationmarker"},
		{"mixed.txt", "paginationmarker boundarytokenextra"},
		{"fragment-only.txt", "paginationmarkerextra boundarytokenextra"},
	} {
		importUnderstandingMemory(t, s, memory.filename, memory.body)
	}

	searchQuery := "query=" + url.QueryEscape("paginationmarker boundarytoken")
	complete, _ := searchPaginationHTTP(t, s, searchQuery+"&limit=100")
	if complete.Total != 4 || len(complete.Items) != 4 {
		t.Fatalf("complete search = total %d with %d items", complete.Total, len(complete.Items))
	}

	pagedIDs := make([]string, 0, 4)
	cursor := ""
	for pageIndex := range 4 {
		query := searchQuery + "&limit=1"
		if cursor != "" {
			query += "&cursor=" + url.QueryEscape(cursor)
		}
		page, nextCursor := searchPaginationHTTP(t, s, query)
		if page.Total != complete.Total || len(page.Items) != 1 {
			t.Fatalf("page %d = total %d with %d items", pageIndex, page.Total, len(page.Items))
		}
		pagedIDs = append(pagedIDs, page.Items[0].Memory.Id.String())
		cursor = nextCursor
		if pageIndex < 3 && cursor == "" {
			t.Fatalf("page %d ended before all results were returned", pageIndex)
		}
		if pageIndex == 3 && cursor != "" {
			t.Fatalf("terminal page returned continuation %q", cursor)
		}
	}
	for index, item := range complete.Items {
		if pagedIDs[index] != item.Memory.Id.String() {
			t.Fatalf("page item %d = %s, want complete result %s", index,
				pagedIDs[index], item.Memory.Id)
		}
	}
}

func searchPaginationHTTP(t *testing.T, s *Server, query string) (api.SearchPage, string) {
	t.Helper()
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v0/memories/search?"+query, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("search page = %d %s", response.Code, response.Body.String())
	}
	var wire struct {
		api.SearchPage
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &wire); err != nil {
		t.Fatalf("decode search page: %v (%s)", err, response.Body.String())
	}
	return wire.SearchPage, wire.NextCursor
}

func TestSearchMemoriesHTTPRejectsInvalidContinuationAndOversizePage(t *testing.T) {
	s, _ := openUnderstandingServer(t, t.TempDir(), config.Defaults())
	importUnderstandingMemory(t, s, "cursor-a.txt", "cursorneedle firstword")
	importUnderstandingMemory(t, s, "cursor-b.txt", "cursorneedle secondword")
	_, cursor := searchPaginationHTTP(t, s,
		"query=cursorneedle&limit=1")
	if cursor == "" {
		t.Fatal("first page did not include continuation")
	}

	for _, path := range []string{
		"/api/v0/memories/search?query=cursorneedle&limit=1&cursor=malformed",
		"/api/v0/memories/search?query=different&limit=1&cursor=" + url.QueryEscape(cursor),
		"/api/v0/memories/search?query=cursorneedle&limit=101",
	} {
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusBadRequest ||
			response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("invalid search request %s = %d, Cache-Control %q, %s",
				path, response.Code, response.Header().Get("Cache-Control"), response.Body.String())
		}
		var problem api.Error
		if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
			t.Fatalf("decode invalid search response: %v", err)
		}
		if problem.Code == "" || problem.Message == "" {
			t.Fatalf("invalid search error = %#v", problem)
		}
	}
}
