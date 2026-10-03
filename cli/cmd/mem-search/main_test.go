package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInvalidInvocation(t *testing.T) {
	for _, args := range [][]string{
		{}, {""}, {" \t\n"}, {"one", "two"}, {"-n", "0", "term"},
		{"-n", "-1", "term"}, {"-n", "9223372036854775808", "term"},
		{"-n", "bad", "term"}, {"--all", "-n", "50", "term"},
		{"--unknown", "term"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(
			t.Context(),
			args,
			&stdout,
			&stderr,
		); code != 2 || stdout.Len() != 0 ||
			stderr.Len() == 0 {
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

func TestOperationalFailures(t *testing.T) {
	httpFailure := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		}),
	)
	defer httpFailure.Close()
	closedServer := httptest.NewServer(http.NotFoundHandler())
	closedServer.Close()

	for _, args := range [][]string{
		{"--server", httpFailure.URL, "term"},
		{"--server", httpFailure.URL, "--all", "term"},
		{"--server", closedServer.URL, "term"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(t.Context(), args, &stdout, &stderr); code != 1 ||
			stdout.Len() != 0 || !strings.HasPrefix(stderr.String(), "mem-search:") {
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

func TestPageFailureIsOperational(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") != "" {
			http.Error(w, "page unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(
			[]byte(
				`{"query_plan":{"query":"needle","terms":["needle"]},"total":2,"items":[{"memory":{"id":"00000000-0000-4000-8000-000000000001","blob_hash":"sha256-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","media_type":"text/plain","byte_size":1,"original_filename":"one.txt","imported_at":"2026-01-01T00:00:00Z"},"excerpt":[]}],"next_cursor":"next"}`,
			),
		)
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), []string{"--server", server.URL, "--all", "needle"},
		&stdout, &stderr); code != 1 || stdout.Len() != 0 ||
		!strings.HasPrefix(stderr.String(), "mem-search:") {
		t.Fatalf("page failure exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
