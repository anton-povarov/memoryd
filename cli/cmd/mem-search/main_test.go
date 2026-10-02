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
		{"-n", "bad", "term"}, {"--all", "term"}, {"--unknown", "term"},
		{"term", "-n", "1"},
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
	for _, url := range []string{httpFailure.URL, closedServer.URL} {
		var stdout, stderr bytes.Buffer
		if code := run(
			t.Context(),
			[]string{"--server", url, "term"},
			&stdout,
			&stderr,
		); code != 1 || stdout.Len() != 0 ||
			!strings.HasPrefix(stderr.String(), "mem-search:") {
			t.Fatalf(
				"server=%s exit=%d stdout=%q stderr=%q",
				url,
				code,
				stdout.String(),
				stderr.String(),
			)
		}
	}
}
