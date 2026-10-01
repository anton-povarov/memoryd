package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/anton-povarov/memoryd/server/internal/logging"
)

const understandingProgressPrefix = "MEMORYD_PROGRESS "
const understandingProgressLimit = 4096

// understandingProgress consumes optional phase records without forwarding
// diagnostic text. os/exec calls Write from one stderr-draining goroutine.
type understandingProgress struct {
	ctx     context.Context
	logger  *logging.Logger
	pending []byte
	discard bool
}

func (progress *understandingProgress) Write(data []byte) (int, error) {
	written := len(data)
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n')
		complete := end >= 0
		if !complete {
			end = len(data)
		}
		if !progress.discard && len(progress.pending)+end <= understandingProgressLimit {
			progress.pending = append(progress.pending, data[:end]...)
		} else {
			progress.pending = progress.pending[:0]
			progress.discard = true
		}
		if !complete {
			break
		}
		if !progress.discard {
			progress.report(progress.pending)
		}
		progress.pending = progress.pending[:0]
		progress.discard = false
		data = data[end+1:]
	}
	return written, nil
}

func (progress *understandingProgress) report(line []byte) {
	payload, matched := bytes.CutPrefix(line, []byte(understandingProgressPrefix))
	if !matched {
		return
	}
	var event struct {
		Phase string  `json:"phase"`
		Turn  *uint64 `json:"turn"`
	}
	if json.Unmarshal(payload, &event) != nil || (event.Turn != nil && *event.Turn == 0) {
		return
	}
	switch event.Phase {
	case "request_validated", "source_staged", "model_runtime_ready", "model_started",
		"model_completed", "result_validated", "completed":
	default:
		return
	}

	attrs := []any{
		slog.String("phase", event.Phase),
	}
	if event.Turn != nil {
		attrs = append(attrs, "turn", *event.Turn)
	}

	progress.logger.InfoContext(progress.ctx,
		fmt.Sprintf("Understanding phase %s", event.Phase),
		attrs...,
	)
}
