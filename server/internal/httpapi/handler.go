// Package httpapi adapts the generated OpenAPI interface to Vault behavior.
// Deferred operations retain deterministic stubs while Memory import, detail,
// Run lookup, browse, and content download use durable state.
package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"strings"
	"time"

	"github.com/anton-povarov/memoryd/server/api"
	"github.com/anton-povarov/memoryd/server/internal/logging"
	"github.com/anton-povarov/memoryd/server/internal/vault"
	"github.com/google/uuid"
)

const (
	stubMemoryID             = "2d6f4d1a-4d62-4ef3-9b2c-6aa7f1f0d6c2"
	stubRunID                = "7eb97a66-59ad-4771-bfc2-0d5a62c55f56"
	stubLogID                = "450369c7-37cf-4322-bbbb-32091834cb88"
	stubHash                 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	multipartFormMemoryBytes = 1 << 20
	completedPercent         = 100.0
	stubByteSize             = 1234
)

var stubTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

type Handler struct {
	version   string
	vaultPath string
	vault     *vault.Vault
}

type understandingEventStream = api.UnderstandingEventStreamTexteventStreamResponse

func NewHandler(version, vaultPath string, memoryVault *vault.Vault) *Handler {
	return &Handler{
		version:   version,
		vaultPath: vaultPath,
		vault:     memoryVault,
	}
}

func (h *Handler) GetLiveness(
	context.Context, api.GetLivenessRequestObject,
) (api.GetLivenessResponseObject, error) {
	return api.GetLiveness200JSONResponse(h.health()), nil
}

func (h *Handler) GetReadiness(
	context.Context, api.GetReadinessRequestObject,
) (api.GetReadinessResponseObject, error) {
	return api.GetReadiness200JSONResponse(h.health()), nil
}

func (h *Handler) BrowseMemories(
	ctx context.Context, request api.BrowseMemoriesRequestObject,
) (api.BrowseMemoriesResponseObject, error) {
	limit := vault.DefaultListLimit
	if request.Params.Limit != nil {
		limit = int(*request.Params.Limit)
	}
	var after *vault.ListCursor
	if request.Params.Cursor != nil {
		decoded, err := decodeBrowseCursor(*request.Params.Cursor)
		if err != nil {
			return api.BrowseMemories400JSONResponse{
				BadRequestJSONResponse: api.BadRequestJSONResponse(api.Error{
					Code: "invalid_cursor", Details: nil,
					ExistingMemory: nil, Message: "cursor is invalid",
				}),
			}, nil
		}
		after = &decoded
	}
	memories, hasMore, err := h.vault.ListMemories(ctx, limit, after)
	if err != nil {
		return nil, err
	}
	items := make([]api.MemorySummary, 0, len(memories))
	for _, memory := range memories {
		items = append(items, memorySummary(memory))
	}
	response := api.BrowseMemories200JSONResponse{
		Items:      items,
		NextCursor: nil,
	}
	if hasMore {
		nextCursor, err := encodeBrowseCursor(memories[len(memories)-1])
		if err != nil {
			return nil, err
		}
		response.NextCursor = &nextCursor
	}
	return response, nil
}

func (h *Handler) GetMemory(
	ctx context.Context, request api.GetMemoryRequestObject,
) (api.GetMemoryResponseObject, error) {
	logger := logging.FromContext(ctx)
	logger.DebugContext(
		ctx,
		"Getting Memory details",
		"memory_id",
		request.MemoryId,
	)
	memory, err := h.vault.Memory(ctx, request.MemoryId)
	if errors.Is(err, vault.ErrMemoryNotFound) {
		logger.DebugContext(
			ctx,
			"Memory details not found",
			"memory_id",
			request.MemoryId,
		)
		return api.GetMemory404JSONResponse{
			NotFoundJSONResponse: api.NotFoundJSONResponse(notFoundError()),
		}, nil
	}
	if err != nil {
		return nil, err
	}
	return api.GetMemory200JSONResponse(memoryDetail(memory)), nil
}

func (h *Handler) GetMemoryContent(
	ctx context.Context,
	request api.GetMemoryContentRequestObject,
) (
	api.GetMemoryContentResponseObject,
	error,
) {
	logger := logging.FromContext(ctx)
	logger.DebugContext(
		ctx,
		"Preparing Memory content download",
		"memory_id",
		request.MemoryId,
	)
	memory, content, err := h.vault.OpenContent(ctx, request.MemoryId)
	if errors.Is(err, vault.ErrMemoryNotFound) {
		logger.InfoContext(
			ctx,
			"Memory content download not found",
			"memory_id",
			request.MemoryId,
		)
		return api.GetMemoryContent404JSONResponse{
			NotFoundJSONResponse: api.NotFoundJSONResponse(notFoundError()),
		}, nil
	}
	if err != nil {
		return nil, err
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{
		"filename": memory.OriginalFilename,
	})
	etag := fmt.Sprintf("%q", memory.BlobHash)
	logger.InfoContext(ctx, "Memory content download prepared",
		"memory_id", memory.ID,
		"blob_hash", memory.BlobHash,
		"byte_size", memory.ByteSize,
		"media_type", memory.MediaType,
	)
	headers := api.GetMemoryContent200ResponseHeaders{
		ContentDisposition: &disposition,
		ETag:               &etag,
	}
	switch memory.MediaType {
	case "application/pdf":
		return api.GetMemoryContent200ApplicationpdfResponse{
			Body: content, Headers: headers, ContentLength: memory.ByteSize,
		}, nil
	case "image/jpeg":
		return api.GetMemoryContent200ImagejpegResponse{
			Body:          content,
			Headers:       headers,
			ContentLength: memory.ByteSize,
		}, nil
	case "image/png":
		return api.GetMemoryContent200ImagepngResponse{
			Body: content, Headers: headers, ContentLength: memory.ByteSize,
		}, nil
	default:
		return api.GetMemoryContent200ApplicationoctetStreamResponse{
			Body: content, Headers: headers, ContentLength: memory.ByteSize,
		}, nil
	}
}

func (h *Handler) ImportMemory(
	ctx context.Context, request api.ImportMemoryRequestObject,
) (api.ImportMemoryResponseObject, error) {
	logger := logging.FromContext(ctx)
	logger.DebugContext(ctx, "Parsing Memory import request")
	if request.Body == nil {
		logger.InfoContext(
			ctx,
			"Memory import rejected",
			"reason",
			"missing multipart body",
		)
		return badImport(
			"invalid_multipart",
			"multipart request body is required",
		), nil
	}
	form, err := request.Body.ReadForm(multipartFormMemoryBytes)
	if err != nil {
		logger.InfoContext(
			ctx,
			"Memory import rejected",
			"reason",
			"invalid multipart body",
			"error",
			err,
		)
		return badImport(
			"invalid_multipart",
			"could not parse multipart request",
		), nil
	}
	defer form.RemoveAll()
	files := form.File["file"]
	if len(files) != 1 {
		logger.InfoContext(
			ctx, "Memory import rejected",
			"reason", "expected exactly one Blob", "blob_count", len(files),
		)
		return badImport("invalid_file", "exactly one Blob is required"), nil
	}
	content, err := files[0].Open()
	if err != nil {
		return nil, err
	}
	defer content.Close() // nolint:errcheck
	candidate := vault.Import{
		Content:            content,
		OriginalFilename:   files[0].Filename,
		RelativePath:       formValue(form.Value, "relative_path"),
		FullPath:           formValue(form.Value, "full_path"),
		FilesystemCreated:  formTime(form.Value, "filesystem_created_at"),
		FilesystemModified: formTime(form.Value, "filesystem_modified_at"),
	}
	memory, err := h.vault.Put(ctx, candidate)
	var duplicate *vault.DuplicateError
	switch {
	case errors.As(err, &duplicate):
		existing := api.DuplicateMemory{
			Id: duplicate.Existing.ID,
			UnderstandingState: api.UnderstandingState(
				duplicate.Existing.UnderstandingState,
			),
		}
		return api.ImportMemory409JSONResponse{
			Code: "duplicate_memory", Details: nil,
			Message: err.Error(), ExistingMemory: &existing,
		}, nil
	case errors.Is(err, vault.ErrBlobTooLarge):
		logger.InfoContext(
			ctx,
			"Memory import rejected",
			"reason",
			"Blob too large",
		)
		return api.ImportMemory413JSONResponse{
			Code: "blob_too_large", Details: nil,
			ExistingMemory: nil, Message: err.Error(),
		}, nil
	case errors.Is(err, vault.ErrUnsupportedContent):
		logger.InfoContext(
			ctx,
			"Memory import rejected",
			"reason",
			"unsupported content",
		)
		return api.ImportMemory415JSONResponse{
			Code: "unsupported_content", Details: nil,
			ExistingMemory: nil, Message: err.Error(),
		}, nil
	case err != nil:
		return nil, err
	}
	message := "Deterministic stub understanding completed"
	percent := completedPercent
	stream, err := eventStream(
		event{
			name: "import_started",
			data: api.UnderstandingProgressEvent{
				Event: api.ImportStarted, MemoryId: memory.ID,
				Message: nil, Percent: nil, Phase: "accepted",
			},
		},
		event{
			name: "understanding_progress",
			data: api.UnderstandingProgressEvent{
				Event: api.UnderstandingProgress, MemoryId: memory.ID,
				Message: &message, Percent: &percent, Phase: "stub",
			},
		},
		event{
			name: "import_completed",
			data: api.ImportCompletedEvent{
				Event: api.ImportCompleted, Memory: memorySummary(memory),
			},
		},
	)
	if err != nil {
		return nil, err
	}
	return api.ImportMemory200TexteventStreamResponse{Body: stream}, nil
}

func (h *Handler) RebuildMemory(
	_ context.Context, request api.RebuildMemoryRequestObject,
) (api.RebuildMemoryResponseObject, error) {
	stream, err := understandingStream(
		request.MemoryId,
		"rebuild_completed",
		api.RebuildCompleted,
		api.Regular,
	)
	if err != nil {
		return nil, err
	}
	return api.RebuildMemory200TexteventStreamResponse{
		UnderstandingEventStreamTexteventStreamResponse: understandingEventStream{
			Body: stream,
		},
	}, nil
}

func (h *Handler) EnhanceMemoryWithCodex(
	_ context.Context, request api.EnhanceMemoryWithCodexRequestObject,
) (api.EnhanceMemoryWithCodexResponseObject, error) {
	stream, err := understandingStream(
		request.MemoryId, "codex_enhancement_completed",
		api.CodexEnhancementCompleted, api.CodexEnhancement,
	)
	if err != nil {
		return nil, err
	}
	return api.EnhanceMemoryWithCodex200TexteventStreamResponse{
		UnderstandingEventStreamTexteventStreamResponse: understandingEventStream{
			Body: stream,
		},
	}, nil
}

func (h *Handler) ListUnderstandingRuns(
	ctx context.Context, request api.ListUnderstandingRunsRequestObject,
) (api.ListUnderstandingRunsResponseObject, error) {
	memory, err := h.vault.Memory(ctx, request.MemoryId)
	if errors.Is(err, vault.ErrMemoryNotFound) {
		return api.ListUnderstandingRuns404JSONResponse{
			NotFoundJSONResponse: api.NotFoundJSONResponse(notFoundError()),
		}, nil
	}
	if err != nil {
		return nil, err
	}
	items := []api.UnderstandingRun{}
	if memory.RunID != nil {
		items = append(items, understandingRun(memory))
	}
	return api.ListUnderstandingRuns200JSONResponse{
		Items: items, NextCursor: nil,
	}, nil
}

func (h *Handler) GetUnderstandingRun(
	ctx context.Context, request api.GetUnderstandingRunRequestObject,
) (api.GetUnderstandingRunResponseObject, error) {
	memory, err := h.vault.Memory(ctx, request.MemoryId)
	if errors.Is(err, vault.ErrMemoryNotFound) ||
		err == nil && (memory.RunID == nil || *memory.RunID != request.RunId) {
		return api.GetUnderstandingRun404JSONResponse{
			NotFoundJSONResponse: api.NotFoundJSONResponse(notFoundError()),
		}, nil
	}
	if err != nil {
		return nil, err
	}
	return api.GetUnderstandingRun200JSONResponse(understandingRun(memory)), nil
}

func (h *Handler) ListProcessingLogs(
	_ context.Context, request api.ListProcessingLogsRequestObject,
) (api.ListProcessingLogsResponseObject, error) {
	return api.ListProcessingLogs200JSONResponse{
		Items:      []api.ProcessingLog{sampleLog(request.MemoryId, nil)},
		NextCursor: nil,
	}, nil
}

func (h *Handler) ListRunLogs(
	_ context.Context, request api.ListRunLogsRequestObject,
) (api.ListRunLogsResponseObject, error) {
	return api.ListRunLogs200JSONResponse{
		Items: []api.ProcessingLog{
			sampleLog(request.MemoryId, &request.RunId),
		},
		NextCursor: nil,
	}, nil
}

func (h *Handler) SearchMemories(
	_ context.Context, request api.SearchMemoriesRequestObject,
) (api.SearchMemoriesResponseObject, error) {
	query := ""
	if request.Body != nil {
		query = request.Body.Query
	}
	return api.SearchMemories200JSONResponse{
		Query: query,
		Plan: api.QueryPlan{
			Explanation: nil, FactFilters: []api.FactFilter{},
			FullTextTerms: strings.Fields(query),
		},
		Items: []api.SearchResult{{
			Memory:       sampleMemory(uuid.MustParse(stubMemoryID)),
			MatchedFacts: []api.Fact{}, MatchedTerms: strings.Fields(query),
		}},
		NextCursor: nil,
	}, nil
}

func (h *Handler) health() api.HealthResponse {
	return api.HealthResponse{
		Status: api.Ok, Version: &h.version, VaultPath: &h.vaultPath,
	}
}

func memorySummary(memory vault.Memory) api.MemorySummary {
	return api.MemorySummary{
		Id:                 memory.ID,
		BlobHash:           memory.BlobHash,
		MediaType:          memory.MediaType,
		ByteSize:           memory.ByteSize,
		OriginalFilename:   memory.OriginalFilename,
		UnderstandingState: api.UnderstandingState(memory.UnderstandingState),
		ActiveRunId:        memory.RunID,
		ImportedAt:         memory.ImportedAt,
		OriginalCreatedAt:  memory.FilesystemCreated,
		OriginalModifiedAt: memory.FilesystemModified,
	}
}

func memoryDetail(memory vault.Memory) api.MemoryDetail {
	contentURL := "/api/v0/memories/" + memory.ID.String() + "/content"
	mediaType := memory.MediaType
	byteSize := memory.ByteSize
	contentHash := memory.BlobHash
	text := "Understanding is not implemented; this is deterministic stub Derived Content."
	context := api.ImportContext{
		ByteSize:             &byteSize,
		ContentHash:          &contentHash,
		FilesystemCreatedAt:  memory.FilesystemCreated,
		FilesystemModifiedAt: memory.FilesystemModified,
		FullPath:             nil,
		MediaType:            &mediaType,
		OriginalFilename:     memory.OriginalFilename,
		RelativePath:         nil,
	}
	if memory.RelativePath != "" {
		context.RelativePath = &memory.RelativePath
	}
	if memory.FullPath != "" {
		context.FullPath = &memory.FullPath
	}
	detail := api.MemoryDetail{
		ActiveRun:     nil,
		Memory:        memorySummary(memory),
		ContentUrl:    &contentURL,
		ImportContext: context,
		Facts: []api.Fact{{
			Confidence: nil, Namespace: "memoryd", Name: "stub",
			Value: true, ValueType: api.Boolean, Origin: "memoryd-stub",
		}},
		DerivedContent: []api.DerivedContent{{
			Kind: "stub", Metadata: nil, Text: &text,
		}},
	}
	if memory.RunID != nil {
		run := understandingRun(memory)
		detail.ActiveRun = &run
	}
	return detail
}

func understandingRun(memory vault.Memory) api.UnderstandingRun {
	completedAt := memory.ImportedAt
	if memory.UnderstandingCompleted != nil {
		completedAt = *memory.UnderstandingCompleted
	}
	return api.UnderstandingRun{
		Id:                *memory.RunID,
		MemoryId:          memory.ID,
		Pipeline:          api.Regular,
		CreatedAt:         memory.ImportedAt,
		CompletedAt:       completedAt,
		Active:            true,
		ExtractorVersions: nil,
		PluginVersions:    nil,
		Warnings:          nil,
	}
}

func notFoundError() api.Error {
	return api.Error{
		Code: "memory_not_found", Details: nil,
		ExistingMemory: nil, Message: "Memory was not found",
	}
}

func badImport(code, message string) api.ImportMemoryResponseObject {
	return api.ImportMemory400JSONResponse{
		BadRequestJSONResponse: api.BadRequestJSONResponse(api.Error{
			Code: code, Details: nil, ExistingMemory: nil, Message: message,
		}),
	}
}

func formValue(values map[string][]string, name string) string {
	if items := values[name]; len(items) != 0 {
		return items[0]
	}
	return ""
}

func formTime(values map[string][]string, name string) *time.Time {
	value := formValue(values, name)
	if value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	return &parsed
}

type browseCursor struct {
	ImportedAt time.Time `json:"imported_at"`
	ID         uuid.UUID `json:"id"`
}

func encodeBrowseCursor(memory vault.Memory) (string, error) {
	encoded, err := json.Marshal(
		browseCursor{ImportedAt: memory.ImportedAt, ID: memory.ID},
	)
	if err != nil {
		return "", fmt.Errorf("encode browse cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeBrowseCursor(value string) (vault.ListCursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return vault.ListCursor{}, err
	}
	var cursor browseCursor
	if err := json.Unmarshal(decoded, &cursor); err != nil {
		return vault.ListCursor{}, err
	}
	if cursor.ImportedAt.IsZero() || cursor.ID == uuid.Nil {
		return vault.ListCursor{}, errors.New("cursor is incomplete")
	}
	return vault.ListCursor{ImportedAt: cursor.ImportedAt, ID: cursor.ID}, nil
}

func sampleMemory(id uuid.UUID) api.MemorySummary {
	runID := uuid.MustParse(stubRunID)
	return api.MemorySummary{
		Id: id, BlobHash: stubHash, MediaType: "application/pdf",
		ByteSize: stubByteSize, OriginalFilename: "memoryd-v0-example.pdf",
		UnderstandingState: api.Done, ActiveRunId: &runID,
		ImportedAt: stubTime, OriginalCreatedAt: &stubTime,
		OriginalModifiedAt: nil,
	}
}

func sampleDetail(id uuid.UUID) api.MemoryDetail {
	contentURL := "/api/v0/memories/" + id.String() + "/content"
	text := "Deterministic v0 Derived Content."
	memory := sampleMemory(id)
	run := sampleRun(id, api.Regular)
	return api.MemoryDetail{
		ActiveRun:  &run,
		Memory:     memory,
		ContentUrl: &contentURL,
		ImportContext: api.ImportContext{
			ByteSize: &memory.ByteSize, ContentHash: &memory.BlobHash,
			FilesystemCreatedAt: nil, FilesystemModifiedAt: nil,
			FullPath: nil, MediaType: &memory.MediaType,
			OriginalFilename: memory.OriginalFilename, RelativePath: nil,
		},
		Facts: []api.Fact{{
			Confidence: nil, Namespace: "memoryd", Name: "stub",
			Value: true, ValueType: api.Boolean, Origin: "memoryd-v0",
		}},
		DerivedContent: []api.DerivedContent{{
			Kind: "extracted_text", Metadata: nil, Text: &text,
		}},
	}
}

func sampleRun(
	memoryID uuid.UUID,
	pipeline api.UnderstandingRunPipeline,
) api.UnderstandingRun {
	return api.UnderstandingRun{
		Id:                uuid.MustParse(stubRunID),
		MemoryId:          memoryID,
		Pipeline:          pipeline,
		CreatedAt:         stubTime,
		CompletedAt:       stubTime.Add(time.Second),
		Active:            true,
		ExtractorVersions: nil,
		PluginVersions:    nil,
		Warnings:          nil,
	}
}

func sampleLog(memoryID uuid.UUID, runID *uuid.UUID) api.ProcessingLog {
	return api.ProcessingLog{
		Id:        uuid.MustParse(stubLogID),
		MemoryId:  memoryID,
		RunId:     runID,
		AttemptId: uuid.MustParse(stubRunID),
		Timestamp: stubTime,
		Kind:      api.ProcessingLogKindLifecycle,
		Message:   "memoryd v0 stub completed",
		Metadata:  nil,
		Truncated: nil,
	}
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

func understandingStream(
	memoryID uuid.UUID,
	eventName string,
	completed api.UnderstandingCompletedEventEvent,
	pipeline api.UnderstandingRunPipeline,
) (io.Reader, error) {
	memory := sampleMemory(memoryID)
	run := sampleRun(memoryID, pipeline)
	return eventStream(
		event{
			name: "understanding_started",
			data: api.UnderstandingProgressEvent{
				Event:    api.UnderstandingStarted,
				MemoryId: memoryID,
				Message:  nil,
				Percent:  nil,
				Phase:    "started",
			},
		},
		event{
			name: eventName,
			data: api.UnderstandingCompletedEvent{
				Event:  completed,
				Memory: memory,
				Run:    run,
			},
		},
	)
}

var _ api.StrictServerInterface = (*Handler)(nil)
