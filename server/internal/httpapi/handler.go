// Package httpapi adapts the generated OpenAPI interface to memoryd behavior.
// This v0 adapter returns deterministic data so clients can exercise the full
// contract before the Vault module exists.
package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/anton-povarov/memoryd/server/api"
	"github.com/google/uuid"
)

const (
	stubMemoryID = "2d6f4d1a-4d62-4ef3-9b2c-6aa7f1f0d6c2"
	stubRunID    = "7eb97a66-59ad-4771-bfc2-0d5a62c55f56"
	stubLogID    = "450369c7-37cf-4322-bbbb-32091834cb88"
	stubHash     = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

var stubTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

type Handler struct {
	version   string
	vaultPath string
}

func NewHandler(version, vaultPath string) *Handler {
	return &Handler{version: version, vaultPath: vaultPath}
}

func (h *Handler) GetLiveness(context.Context, api.GetLivenessRequestObject) (api.GetLivenessResponseObject, error) {
	return api.GetLiveness200JSONResponse(h.health()), nil
}

func (h *Handler) GetReadiness(context.Context, api.GetReadinessRequestObject) (api.GetReadinessResponseObject, error) {
	return api.GetReadiness200JSONResponse(h.health()), nil
}

func (h *Handler) BrowseMemories(context.Context, api.BrowseMemoriesRequestObject) (api.BrowseMemoriesResponseObject, error) {
	return api.BrowseMemories200JSONResponse{Items: []api.MemorySummary{sampleMemory(uuid.MustParse(stubMemoryID))}}, nil
}

func (h *Handler) GetMemory(_ context.Context, request api.GetMemoryRequestObject) (api.GetMemoryResponseObject, error) {
	return api.GetMemory200JSONResponse(sampleDetail(request.MemoryId)), nil
}

func (h *Handler) GetMemoryContent(_ context.Context, request api.GetMemoryContentRequestObject) (api.GetMemoryContentResponseObject, error) {
	body := []byte("memoryd v0 stub content\n")
	disposition := `attachment; filename="memoryd-v0-stub.txt"`
	etag := fmt.Sprintf("%q", stubHash)
	return api.GetMemoryContent200ApplicationoctetStreamResponse{
		Body: bytes.NewReader(body),
		Headers: api.GetMemoryContent200ResponseHeaders{
			ContentDisposition: &disposition,
			ETag:               &etag,
		},
		ContentLength: int64(len(body)),
	}, nil
}

func (h *Handler) ImportMemory(context.Context, api.ImportMemoryRequestObject) (api.ImportMemoryResponseObject, error) {
	memory := sampleMemory(uuid.MustParse(stubMemoryID))
	stream, err := eventStream(
		event{"import_started", api.UnderstandingProgressEvent{Event: api.ImportStarted, MemoryId: memory.Id, Phase: "accepted"}},
		event{"import_completed", api.ImportCompletedEvent{Event: api.ImportCompleted, Memory: memory}},
	)
	if err != nil {
		return nil, err
	}
	return api.ImportMemory200TexteventStreamResponse{Body: stream}, nil
}

func (h *Handler) RebuildMemory(_ context.Context, request api.RebuildMemoryRequestObject) (api.RebuildMemoryResponseObject, error) {
	stream, err := understandingStream(request.MemoryId, "rebuild_completed", api.RebuildCompleted, api.Regular)
	if err != nil {
		return nil, err
	}
	return api.RebuildMemory200TexteventStreamResponse{UnderstandingEventStreamTexteventStreamResponse: api.UnderstandingEventStreamTexteventStreamResponse{Body: stream}}, nil
}

func (h *Handler) EnhanceMemoryWithCodex(_ context.Context, request api.EnhanceMemoryWithCodexRequestObject) (api.EnhanceMemoryWithCodexResponseObject, error) {
	stream, err := understandingStream(request.MemoryId, "codex_enhancement_completed", api.CodexEnhancementCompleted, api.CodexEnhancement)
	if err != nil {
		return nil, err
	}
	return api.EnhanceMemoryWithCodex200TexteventStreamResponse{UnderstandingEventStreamTexteventStreamResponse: api.UnderstandingEventStreamTexteventStreamResponse{Body: stream}}, nil
}

func (h *Handler) ListUnderstandingRuns(_ context.Context, request api.ListUnderstandingRunsRequestObject) (api.ListUnderstandingRunsResponseObject, error) {
	return api.ListUnderstandingRuns200JSONResponse{Items: []api.UnderstandingRun{sampleRun(request.MemoryId, api.Regular)}}, nil
}

func (h *Handler) GetUnderstandingRun(_ context.Context, request api.GetUnderstandingRunRequestObject) (api.GetUnderstandingRunResponseObject, error) {
	run := sampleRun(request.MemoryId, api.Regular)
	run.Id = request.RunId
	return api.GetUnderstandingRun200JSONResponse(run), nil
}

func (h *Handler) ListProcessingLogs(_ context.Context, request api.ListProcessingLogsRequestObject) (api.ListProcessingLogsResponseObject, error) {
	return api.ListProcessingLogs200JSONResponse{Items: []api.ProcessingLog{sampleLog(request.MemoryId, nil)}}, nil
}

func (h *Handler) ListRunLogs(_ context.Context, request api.ListRunLogsRequestObject) (api.ListRunLogsResponseObject, error) {
	return api.ListRunLogs200JSONResponse{Items: []api.ProcessingLog{sampleLog(request.MemoryId, &request.RunId)}}, nil
}

func (h *Handler) SearchMemories(_ context.Context, request api.SearchMemoriesRequestObject) (api.SearchMemoriesResponseObject, error) {
	query := ""
	if request.Body != nil {
		query = request.Body.Query
	}
	return api.SearchMemories200JSONResponse{
		Query: query,
		Plan:  api.QueryPlan{FactFilters: []api.FactFilter{}, FullTextTerms: strings.Fields(query)},
		Items: []api.SearchResult{{Memory: sampleMemory(uuid.MustParse(stubMemoryID)), MatchedFacts: []api.Fact{}, MatchedTerms: strings.Fields(query)}},
	}, nil
}

func (h *Handler) health() api.HealthResponse {
	return api.HealthResponse{Status: api.Ok, Version: &h.version, VaultPath: &h.vaultPath}
}

func sampleMemory(id uuid.UUID) api.MemorySummary {
	runID := uuid.MustParse(stubRunID)
	return api.MemorySummary{Id: id, BlobHash: stubHash, MediaType: "application/pdf", ByteSize: 1234, OriginalFilename: "memoryd-v0-example.pdf", UnderstandingState: api.Done, ActiveRunId: &runID, ImportedAt: stubTime, OriginalCreatedAt: &stubTime}
}

func sampleDetail(id uuid.UUID) api.MemoryDetail {
	contentURL := "/api/v0/memories/" + id.String() + "/content"
	text := "Deterministic v0 Derived Content."
	memory := sampleMemory(id)
	run := sampleRun(id, api.Regular)
	return api.MemoryDetail{
		Memory:         memory,
		ContentUrl:     &contentURL,
		ImportContext:  api.ImportContext{OriginalFilename: memory.OriginalFilename, MediaType: &memory.MediaType, ByteSize: &memory.ByteSize, ContentHash: &memory.BlobHash},
		Facts:          []api.Fact{{Namespace: "memoryd", Name: "stub", Value: true, ValueType: api.Boolean, Origin: "memoryd-v0"}},
		DerivedContent: []api.DerivedContent{{Kind: "extracted_text", Text: &text}},
		ActiveRun:      &run,
	}
}

func sampleRun(memoryID uuid.UUID, pipeline api.UnderstandingRunPipeline) api.UnderstandingRun {
	return api.UnderstandingRun{Id: uuid.MustParse(stubRunID), MemoryId: memoryID, Pipeline: pipeline, CreatedAt: stubTime, CompletedAt: stubTime.Add(time.Second), Active: true}
}

func sampleLog(memoryID uuid.UUID, runID *uuid.UUID) api.ProcessingLog {
	return api.ProcessingLog{Id: uuid.MustParse(stubLogID), MemoryId: memoryID, RunId: runID, AttemptId: uuid.MustParse(stubRunID), Timestamp: stubTime, Kind: api.ProcessingLogKindLifecycle, Message: "memoryd v0 stub completed"}
}

type event struct {
	name string
	data any
}

func eventStream(events ...event) (io.Reader, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	for _, item := range events {
		fmt.Fprintf(&output, "event: %s\ndata: ", item.name)
		if err := encoder.Encode(item.data); err != nil {
			return nil, err
		}
		output.WriteByte('\n')
	}
	return bytes.NewReader(output.Bytes()), nil
}

func understandingStream(memoryID uuid.UUID, eventName string, completed api.UnderstandingCompletedEventEvent, pipeline api.UnderstandingRunPipeline) (io.Reader, error) {
	memory := sampleMemory(memoryID)
	run := sampleRun(memoryID, pipeline)
	return eventStream(
		event{"understanding_started", api.UnderstandingProgressEvent{Event: api.UnderstandingStarted, MemoryId: memoryID, Phase: "started"}},
		event{eventName, api.UnderstandingCompletedEvent{Event: completed, Memory: memory, Run: run}},
	)
}

var _ api.StrictServerInterface = (*Handler)(nil)
