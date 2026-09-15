// Package vault owns durable Memories and their immutable Blob content.
package vault

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anton-povarov/memoryd/server/internal/logging"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

const MaxBlobBytes int64 = 100 << 20

var (
	ErrBlobTooLarge       = errors.New("Blob exceeds the 100 MiB limit")
	ErrUnsupportedContent = errors.New("Blob content is not a supported PDF, JPEG, or PNG")
	ErrMemoryNotFound     = errors.New("Memory not found")
)

type State string

const (
	StateInProgress State = "InProgress"
	StateDone       State = "Done"
	StateFailed     State = "Failed"
)

type Import struct {
	Content            io.Reader
	OriginalFilename   string
	RelativePath       string
	FullPath           string
	FilesystemCreated  *time.Time
	FilesystemModified *time.Time
}

type Memory struct {
	ID                     uuid.UUID
	BlobHash               string
	OriginalFilename       string
	RelativePath           string
	FullPath               string
	FilesystemCreated      *time.Time
	FilesystemModified     *time.Time
	MediaType              string
	ByteSize               int64
	ImportedAt             time.Time
	UnderstandingState     State
	RunID                  *uuid.UUID
	UnderstandingCompleted *time.Time
}

// ListCursor identifies the last Memory observed by a caller. It is kept
// transport-neutral; HTTP encoding remains the adapter's responsibility.
type ListCursor struct {
	ImportedAt time.Time
	ID         uuid.UUID
}

type DuplicateError struct{ Existing Memory }

func (e *DuplicateError) Error() string {
	return "Duplicate Blob already belongs to Memory " + e.Existing.ID.String()
}

type Vault struct {
	db      *sql.DB
	blobDir string
}

func Open(ctx context.Context, databasePath, blobDir string) (*Vault, error) {
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	contentDir := filepath.Join(blobDir, "sha256")
	if err := os.MkdirAll(contentDir, 0o755); err != nil {
		return nil, fmt.Errorf("create Blob directory: %w", err)
	}

	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return nil, fmt.Errorf("open Vault database: %w", err)
	}
	db.SetMaxOpenConns(1)
	v := &Vault{db: db, blobDir: contentDir}
	if err := v.initialize(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return v, nil
}

func (v *Vault) initialize(ctx context.Context) error {
	const schema = `CREATE TABLE IF NOT EXISTS memories (
		id TEXT PRIMARY KEY,
		blob_hash TEXT NOT NULL UNIQUE,
		original_filename TEXT NOT NULL,
		relative_path TEXT,
		full_path TEXT,
		filesystem_created_at TEXT,
		filesystem_modified_at TEXT,
		media_type TEXT NOT NULL,
		byte_size INTEGER NOT NULL,
		imported_at TEXT NOT NULL,
		understanding_state TEXT NOT NULL,
		run_id TEXT,
		understanding_completed_at TEXT
	)`
	if _, err := v.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("initialize Vault database: %w", err)
	}
	return nil
}

func (v *Vault) Close() error { return v.db.Close() }

func (v *Vault) Put(ctx context.Context, candidate Import) (Memory, error) {
	if candidate.Content == nil {
		return Memory{}, errors.New("Blob content is required")
	}
	logger := logging.FromContext(ctx)
	logger.DebugContext(ctx, "Blob import staging started", "original_filename", safeFilename(candidate.OriginalFilename))

	temporary, err := os.CreateTemp(v.blobDir, ".import-*")
	if err != nil {
		return Memory{}, fmt.Errorf("create temporary Blob: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	hash := sha256.New()
	byteSize, copyErr := copyWithLimit(io.MultiWriter(temporary, hash), candidate.Content, MaxBlobBytes)
	closeErr := temporary.Close()
	if copyErr != nil {
		return Memory{}, copyErr
	}
	if closeErr != nil {
		return Memory{}, fmt.Errorf("close temporary Blob: %w", closeErr)
	}

	mediaType, err := detectMediaType(temporaryPath)
	if err != nil {
		return Memory{}, err
	}
	blobHash := hex.EncodeToString(hash.Sum(nil))
	logger.DebugContext(ctx, "Blob import staged",
		"blob_hash", blobHash,
		"byte_size", byteSize,
		"media_type", mediaType,
	)
	finalPath := v.blobPath(blobHash)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		return Memory{}, fmt.Errorf("create Blob shard directory: %w", err)
	}
	if _, err := os.Stat(finalPath); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(temporaryPath, finalPath); err != nil {
			return Memory{}, fmt.Errorf("publish Blob: %w", err)
		}
		logger.DebugContext(ctx, "Blob published", "blob_hash", blobHash)
	} else if err != nil {
		return Memory{}, fmt.Errorf("inspect Blob destination: %w", err)
	}

	now := time.Now().UTC()
	memory := Memory{
		ID:                 uuid.New(),
		BlobHash:           blobHash,
		OriginalFilename:   safeFilename(candidate.OriginalFilename),
		RelativePath:       candidate.RelativePath,
		FullPath:           candidate.FullPath,
		FilesystemCreated:  normalizeTime(candidate.FilesystemCreated),
		FilesystemModified: normalizeTime(candidate.FilesystemModified),
		MediaType:          mediaType,
		ByteSize:           byteSize,
		ImportedAt:         now,
		UnderstandingState: StateInProgress,
	}
	// The Blob upload is complete now. Committing the Memory and finishing the
	// deterministic understanding stub must survive client disconnection.
	durableContext := context.WithoutCancel(ctx)
	result, err := v.db.ExecContext(durableContext, `INSERT INTO memories (
		id, blob_hash, original_filename, relative_path, full_path,
		filesystem_created_at, filesystem_modified_at, media_type, byte_size,
		imported_at, understanding_state
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(blob_hash) DO NOTHING`,
		memory.ID.String(), memory.BlobHash, memory.OriginalFilename,
		nullableString(memory.RelativePath), nullableString(memory.FullPath), nullableTime(memory.FilesystemCreated),
		nullableTime(memory.FilesystemModified), memory.MediaType, memory.ByteSize,
		memory.ImportedAt.Format(time.RFC3339Nano), string(memory.UnderstandingState),
	)
	if err != nil {
		return Memory{}, fmt.Errorf("commit Memory: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return Memory{}, fmt.Errorf("inspect Memory commit: %w", err)
	}
	if inserted == 0 {
		existing, err := v.memoryByHash(durableContext, blobHash)
		if err != nil {
			return Memory{}, err
		}
		logger.InfoContext(durableContext, "Duplicate import rejected",
			"memory_id", existing.ID,
			"blob_hash", existing.BlobHash,
			"understanding_state", existing.UnderstandingState,
		)
		return Memory{}, &DuplicateError{Existing: existing}
	}

	runID := uuid.New()
	completed := time.Now().UTC()
	if _, err := v.db.ExecContext(durableContext, `UPDATE memories SET understanding_state = ?, run_id = ?, understanding_completed_at = ? WHERE id = ?`,
		string(StateDone), runID.String(), completed.Format(time.RFC3339Nano), memory.ID.String()); err != nil {
		return Memory{}, fmt.Errorf("complete stub understanding: %w", err)
	}
	memory.UnderstandingState = StateDone
	memory.RunID = &runID
	memory.UnderstandingCompleted = &completed
	logger.InfoContext(durableContext, "Memory imported",
		"memory_id", memory.ID,
		"blob_hash", memory.BlobHash,
		"byte_size", memory.ByteSize,
		"media_type", memory.MediaType,
		"understanding_state", memory.UnderstandingState,
		"run_id", runID,
	)
	return memory, nil
}

func (v *Vault) Memory(ctx context.Context, id uuid.UUID) (Memory, error) {
	return scanMemory(v.db.QueryRowContext(ctx, selectMemory+` WHERE id = ?`, id.String()))
}

func (v *Vault) ListMemories(ctx context.Context, limit int, after *ListCursor) ([]Memory, bool, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	query := selectMemory
	arguments := []any{}
	if after != nil {
		query += ` WHERE julianday(imported_at) < julianday(?) OR (julianday(imported_at) = julianday(?) AND id < ?)`
		encodedTime := after.ImportedAt.Format(time.RFC3339Nano)
		arguments = append(arguments, encodedTime, encodedTime, after.ID.String())
	}
	query += ` ORDER BY julianday(imported_at) DESC, id DESC LIMIT ?`
	arguments = append(arguments, limit+1)
	rows, err := v.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, false, fmt.Errorf("list Memories: %w", err)
	}
	defer rows.Close()
	var memories []Memory
	for rows.Next() {
		memory, err := scanMemory(rows)
		if err != nil {
			return nil, false, err
		}
		memories = append(memories, memory)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(memories) > limit
	if hasMore {
		memories = memories[:limit]
	}
	return memories, hasMore, nil
}

func (v *Vault) OpenContent(ctx context.Context, id uuid.UUID) (Memory, io.ReadCloser, error) {
	logger := logging.FromContext(ctx)
	logger.DebugContext(ctx, "Opening Memory Blob", "memory_id", id)
	memory, err := v.Memory(ctx, id)
	if err != nil {
		return Memory{}, nil, err
	}
	content, err := os.Open(v.blobPath(memory.BlobHash))
	if err != nil {
		return Memory{}, nil, fmt.Errorf("open Blob content: %w", err)
	}
	logger.DebugContext(ctx, "Memory Blob opened",
		"memory_id", memory.ID,
		"blob_hash", memory.BlobHash,
		"byte_size", memory.ByteSize,
		"media_type", memory.MediaType,
	)
	return memory, content, nil
}

func (v *Vault) blobPath(digest string) string {
	return filepath.Join(v.blobDir, digest[:2], digest[2:4], "sha256-"+digest)
}

const selectMemory = `SELECT id, blob_hash, original_filename, relative_path, full_path,
	filesystem_created_at, filesystem_modified_at, media_type, byte_size, imported_at,
	understanding_state, run_id, understanding_completed_at FROM memories`

type rowScanner interface{ Scan(...any) error }

func scanMemory(row rowScanner) (Memory, error) {
	var memory Memory
	var id, state, importedAt string
	var relativePath, fullPath, createdAt, modifiedAt, runID, completedAt sql.NullString
	err := row.Scan(&id, &memory.BlobHash, &memory.OriginalFilename, &relativePath, &fullPath,
		&createdAt, &modifiedAt, &memory.MediaType, &memory.ByteSize, &importedAt,
		&state, &runID, &completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Memory{}, ErrMemoryNotFound
	}
	if err != nil {
		return Memory{}, fmt.Errorf("read Memory: %w", err)
	}
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return Memory{}, fmt.Errorf("read Memory ID: %w", err)
	}
	memory.ID = parsedID
	memory.RelativePath = relativePath.String
	memory.FullPath = fullPath.String
	memory.ImportedAt, err = time.Parse(time.RFC3339Nano, importedAt)
	if err != nil {
		return Memory{}, fmt.Errorf("read import time: %w", err)
	}
	memory.UnderstandingState = State(state)
	if memory.FilesystemCreated, err = parseNullableTime(createdAt); err != nil {
		return Memory{}, err
	}
	if memory.FilesystemModified, err = parseNullableTime(modifiedAt); err != nil {
		return Memory{}, err
	}
	if runID.Valid {
		value, parseErr := uuid.Parse(runID.String)
		if parseErr != nil {
			return Memory{}, fmt.Errorf("read Run ID: %w", parseErr)
		}
		memory.RunID = &value
	}
	if memory.UnderstandingCompleted, err = parseNullableTime(completedAt); err != nil {
		return Memory{}, err
	}
	return memory, nil
}

func (v *Vault) memoryByHash(ctx context.Context, hash string) (Memory, error) {
	return scanMemory(v.db.QueryRowContext(ctx, selectMemory+` WHERE blob_hash = ?`, hash))
}

func copyWithLimit(destination io.Writer, source io.Reader, limit int64) (int64, error) {
	written, err := io.Copy(destination, io.LimitReader(source, limit))
	if err != nil {
		return written, fmt.Errorf("copy Blob: %w", err)
	}
	if written < limit {
		return written, nil
	}
	var extra [1]byte
	n, err := source.Read(extra[:])
	if err != nil && !errors.Is(err, io.EOF) {
		return written, fmt.Errorf("check Blob size: %w", err)
	}
	if n != 0 {
		return written, ErrBlobTooLarge
	}
	return written, nil
}

func detectMediaType(path string) (string, error) {
	content, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("inspect Blob content: %w", err)
	}
	defer content.Close()
	header := make([]byte, 16)
	n, err := io.ReadFull(content, header)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", fmt.Errorf("inspect Blob content: %w", err)
	}
	header = header[:n]
	switch {
	case len(header) >= 5 && string(header[:5]) == "%PDF-":
		return "application/pdf", nil
	case len(header) >= 3 && header[0] == 0xff && header[1] == 0xd8 && header[2] == 0xff:
		return "image/jpeg", nil
	case len(header) >= 8 && string(header[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png", nil
	default:
		return "", ErrUnsupportedContent
	}
}

func safeFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." || name == "/" {
		return "memory"
	}
	return name
}

func normalizeTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	normalized := value.UTC()
	return &normalized
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.Format(time.RFC3339Nano)
}

func parseNullableTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil, fmt.Errorf("read timestamp: %w", err)
	}
	return &parsed, nil
}
