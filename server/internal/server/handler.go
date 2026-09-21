package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/anton-povarov/memoryd/server/internal/api"
	"github.com/anton-povarov/memoryd/server/internal/logging"
	"github.com/anton-povarov/memoryd/server/internal/vault"
	"github.com/google/uuid"
)

const (
	multipartFormMemoryBytes        = 1 << 20
	multipartProtocolOverhead       = 1 << 20
	MaxImportRequestBytes     int64 = vault.MaxBlobBytes + multipartProtocolOverhead
)

var (
	errUploadTooLarge     = fmt.Errorf("upload limit: %d bytes", MaxImportRequestBytes)
	errUploadVarsTooLarge = fmt.Errorf("upload vars limit: %d bytes", multipartFormMemoryBytes)
)

type Handler struct {
	version   string
	vaultPath string
	vault     *vault.Vault
}

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
				Code: "invalid_cursor", Details: nil,
				ExistingMemory: nil, Message: "cursor is invalid",
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
		return api.GetMemory404JSONResponse(notFoundError()), nil
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
		return api.GetMemoryContent404JSONResponse(notFoundError()), nil
	}
	if errors.Is(err, vault.ErrBlobUnavailable) {
		logger.ErrorContext(
			ctx,
			"Memory Blob is unavailable",
			"memory_id",
			request.MemoryId,
			"error",
			err,
		)
		return api.GetMemoryContent500JSONResponse{
			Code: "blob_unavailable", Details: nil,
			ExistingMemory: nil, Message: err.Error(),
		}, nil
	}
	if err != nil {
		return nil, err
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{
		"filename": memory.ImportContext.OriginalFilename,
	})
	etag := fmt.Sprintf("%q", memory.BlobRef.String())
	logger.InfoContext(ctx, "Memory content download prepared",
		"memory_id", memory.ID,
		"blob_hash", memory.BlobRef.String(),
		"byte_size", memory.ByteSize,
		"media_type", memory.MediaType,
	)
	headers := api.GetMemoryContent200ResponseHeaders{
		ContentDisposition: &disposition,
		ETag:               &etag,
	}
	return api.GetMemoryContent200AsteriskResponse{
		Body:          content,
		Headers:       headers,
		ContentType:   memory.MediaType,
		ContentLength: memory.ByteSize,
	}, nil
}

func (h *Handler) ImportMemory(
	ctx context.Context, request api.ImportMemoryRequestObject,
) (api.ImportMemoryResponseObject, error) {
	logger := logging.FromContext(ctx)
	logger.DebugContext(ctx, "Parsing Memory import request")
	if request.Body == nil {
		logger.InfoContext(ctx,
			"Memory import rejected",
			"reason", "missing multipart body",
		)
		return badImport("invalid_multipart", "multipart request body is required"), nil
	}

	httpRequest, ok := ctx.Value(requestContextKey{}).(*http.Request)
	if !ok {
		return nil, errors.New("HTTP request missing from context")
	}

	logger.Debug(
		"checking upload size",
		"size", httpRequest.ContentLength,
		"limit", MaxImportRequestBytes)

	if httpRequest.ContentLength > MaxImportRequestBytes {
		logger.InfoContext(ctx,
			"Memory import rejected",
			"reason", "upload too large",
			"size", httpRequest.ContentLength,
			"limit", MaxImportRequestBytes,
		)
		return uploadTooLarge(errUploadTooLarge.Error()), nil
	}

	form, err := request.Body.ReadForm(multipartFormMemoryBytes)
	if err != nil {
		if errors.Is(err, multipart.ErrMessageTooLarge) {
			logger.InfoContext(ctx,
				"Memory import rejected",
				"reason", "upload vars too large",
				"limit", multipartFormMemoryBytes,
			)
			return uploadTooLarge(errUploadVarsTooLarge.Error()), nil
		}

		logger.InfoContext(ctx,
			"Memory import rejected",
			"reason", "invalid multipart body",
			"error", err,
		)
		return badImport("invalid_multipart", "could not parse multipart request"), nil
	}
	defer func() {
		if err := form.RemoveAll(); err != nil {
			logger.WarnContext(ctx, "Could not remove multipart files", "error", err)
		}
	}()

	files := form.File["file"]
	if len(files) != 1 {
		logger.InfoContext(ctx,
			"Memory import rejected",
			"reason", "expected exactly one Blob",
			"blob_count", len(files),
		)
		return badImport("invalid_file", "exactly one Blob is required"), nil
	}

	formFile := files[0]
	if strings.TrimSpace(formFile.Filename) == "" {
		return badImport("invalid_file", "file part filename is required"), nil
	}

	// imported file content
	content, err := formFile.Open()
	if err != nil {
		return importInternalError(ctx, err), nil
	}
	defer func() { _ = content.Close() }()

	// import_context from the client
	contextValues := form.Value["import_context"]
	if len(contextValues) > 1 {
		return badImport(
			"invalid_import_context",
			"at most one import_context JSON part is allowed",
		), nil
	}
	var input vault.ImportContextInput
	if len(contextValues) == 1 {
		decoder := json.NewDecoder(strings.NewReader(contextValues[0]))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			return badImport("invalid_import_context", "invalid import_context JSON"), nil
		}
	}
	input.OriginalFilename = formFile.Filename
	importContext, err := vault.ParseImportContext(input)
	if err != nil {
		return badImport("invalid_import_context", err.Error()), nil
	}

	// import the file and see what happens
	candidate := vault.Import{
		Content:           content,
		DeclaredMediaType: formFile.Header.Get("Content-Type"),
		Context:           importContext,
	}
	memory, err := h.vault.Put(ctx, candidate)

	var duplicate *vault.DuplicateError
	switch {
	case errors.As(err, &duplicate):
		existing := api.DuplicateMemory{
			Id: duplicate.Existing.ID,
		}
		return api.ImportMemory409JSONResponse{
			Code:           "duplicate_memory",
			Details:        nil,
			Message:        err.Error(),
			ExistingMemory: &existing,
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

	case errors.Is(err, vault.ErrInvalidMediaType):
		logger.InfoContext(
			ctx,
			"Memory import rejected",
			"reason", "invalid multipart file Content-Type",
		)
		return api.ImportMemory400JSONResponse{
			Code: "invalid_media_type", Details: nil,
			ExistingMemory: nil, Message: err.Error(),
		}, nil

	case err != nil:
		return importInternalError(ctx, err), nil
	}

	return api.ImportMemory201JSONResponse(memorySummary(memory)), nil
}

func importInternalError(ctx context.Context, err error) api.ImportMemoryResponseObject {
	logging.FromContext(ctx).ErrorContext(ctx, "Memory import failed", "error", err)
	return api.ImportMemory500JSONResponse{
		Code: "import_failed", Details: nil,
		ExistingMemory: nil, Message: err.Error(),
	}
}

func (h *Handler) health() api.HealthResponse {
	return api.HealthResponse{
		Status: api.Ok, Version: &h.version, VaultPath: &h.vaultPath,
	}
}

func memorySummary(memory vault.Memory) api.MemorySummary {
	return api.MemorySummary{
		Id:                 memory.ID,
		BlobHash:           memory.BlobRef.String(),
		MediaType:          memory.MediaType,
		ByteSize:           memory.ByteSize,
		OriginalFilename:   memory.ImportContext.OriginalFilename,
		ImportedAt:         memory.ImportedAt,
		OriginalCreatedAt:  memory.ImportContext.FilesystemCreated,
		OriginalModifiedAt: memory.ImportContext.FilesystemModified,
	}
}

func memoryDetail(memory vault.Memory) api.MemoryDetail {
	contentURL := api.ServerUrlLocalMemorydServer + "/memories/" + memory.ID.String() + "/content"
	mediaType := memory.MediaType
	byteSize := memory.ByteSize
	contentHash := memory.BlobRef.String()
	context := api.ImportContext{
		ByteSize:             &byteSize,
		ContentHash:          &contentHash,
		FilesystemCreatedAt:  memory.ImportContext.FilesystemCreated,
		FilesystemModifiedAt: memory.ImportContext.FilesystemModified,
		FullPath:             nil,
		MediaType:            &mediaType,
		OriginalFilename:     memory.ImportContext.OriginalFilename,
		RelativePath:         nil,
	}
	if memory.ImportContext.RelativePath != "" {
		context.RelativePath = &memory.ImportContext.RelativePath
	}
	if memory.ImportContext.FullPath != "" {
		context.FullPath = &memory.ImportContext.FullPath
	}
	return api.MemoryDetail{
		Memory:        memorySummary(memory),
		ContentUrl:    &contentURL,
		ImportContext: context,
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
		Code: code, Details: nil, ExistingMemory: nil, Message: message,
	}
}

func uploadTooLarge(message string) api.ImportMemoryResponseObject {
	return api.ImportMemory413JSONResponse{
		Code:           "blob_too_large",
		Details:        nil,
		ExistingMemory: nil,
		Message:        message,
	}
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

var _ api.StrictServerInterface = (*Handler)(nil)
