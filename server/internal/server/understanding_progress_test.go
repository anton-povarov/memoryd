package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anton-povarov/memoryd/server/internal/logging"
	"github.com/anton-povarov/memoryd/server/internal/vault"
)

func TestUnderstandingProgressFiltersDiagnosticsAndRecoversAfterOversizedLine(t *testing.T) {
	const private = "private-source-and-model-content"
	var output bytes.Buffer
	progress := understandingProgress{
		ctx: context.Background(),
		logger: logging.NewFromSlog(slog.New(slog.NewJSONHandler(
			&output, &slog.HandlerOptions{Level: slog.LevelDebug},
		))),
		pending: nil,
		discard: false,
	}
	stream := private + "\n" +
		strings.Repeat(private, understandingProgressLimit) +
		understandingProgressPrefix + `{"phase":"completed"}` + "\n" +
		understandingProgressPrefix + `{"phase":"model_started","turn":-1}` + "\n" +
		understandingProgressPrefix + `{"phase":"` + private + `"}` + "\n" +
		understandingProgressPrefix + `{"phase":"model_started","turn":2,"text":"` + private + `"}` + "\n" +
		understandingProgressPrefix + `{"phase":"result_validated"}` + "\n"
	for len(stream) > 0 {
		chunk := min(7, len(stream))
		if written, err := progress.Write([]byte(stream[:chunk])); err != nil || written != chunk {
			t.Fatalf("stderr drain = %d, %v", written, err)
		}
		stream = stream[chunk:]
	}
	if strings.Contains(output.String(), private) {
		t.Fatal("private stderr content escaped into progress logs")
	}
	decoder := json.NewDecoder(&output)
	var first, second map[string]any
	if err := decoder.Decode(&first); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&second); err != nil {
		t.Fatal(err)
	}
	if first["phase"] != "model_started" || first["turn"] != float64(2) ||
		second["phase"] != "result_validated" {
		t.Fatalf("phase framing or filtering failed: %#v, %#v", first, second)
	}
	var extra map[string]any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("ignored diagnostics generated an extra event: %#v, %v", extra, err)
	}
}

func TestPluginStderrLoggedOnlyOnPluginFailure(t *testing.T) {
	const private = "private-plugin-diagnostic-content"
	for _, failed := range []bool{false, true} {
		status := "done"
		body := "cat > /dev/null\nprintf '" + private + "\\n' >&2\n"
		if failed {
			status = "failed"
			body += "exit 7\n"
		} else {
			body += "printf '%s\\n' '" + completeUnderstandingResult + "'\n"
		}
		t.Run(status, func(t *testing.T) {
			root := t.TempDir()
			plugin := writeUnderstandingPlugin(t, root, "extract", body)
			var output bytes.Buffer
			logger := logging.NewFromSlog(slog.New(slog.NewJSONHandler(
				&output, &slog.HandlerOptions{Level: slog.LevelDebug},
			)))
			memoryVault, err := vault.Open(context.Background(), logger,
				filepath.Join(root, "memoryd.sqlite"),
				filepath.Join(root, "blobs"), filepath.Join(root, "uploads"),
			)
			if err != nil {
				t.Fatal(err)
			}
			s, err := New("test", understandingPluginConfig(plugin), logger, memoryVault)
			if err != nil {
				_ = memoryVault.Close()
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = s.Close()
				_ = memoryVault.Close()
			})
			memory := importUnderstandingMemory(
				t,
				s,
				"invoice.pdf",
				"%PDF-1.7\nprivate fixture\n%%EOF\n",
			)
			understandingMemoryDetail(t, s, memory, status)
			closeUnderstandingServer(t, s, memoryVault)
			if strings.Contains(output.String(), private) != failed {
				t.Fatalf("stderr logging violated failure-only privacy policy (failed=%t)", failed)
			}
		})
	}
}
