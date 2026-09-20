// Package vault owns durable Memories and their immutable Blob content.
package vault

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/anton-povarov/memoryd/server/internal/logging"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

const MaxBlobBytes int64 = 100 << 20

const (
	DefaultListLimit   = 50
	MaxListLimit       = 100
	maxOpenConnections = 1
	firstShardEnd      = 2
	secondShardEnd     = 4
	overflowProbeBytes = 1
	asciiControlLimit  = 0x20
	asciiDelete        = 0x7f
)

var (
	ErrBlobTooLarge    = errors.New("blob exceeds the 100 MiB limit")
	ErrBlobUnavailable = errors.New("blob is missing or corrupt")
	ErrMemoryNotFound  = errors.New("memory not found")
)

type Import struct {
	Content           io.Reader
	DeclaredMediaType string
	Context           ImportContext
}

// ImportContext preserves metadata observed by the importing client. Missing
// paths and timestamps remain absent; they are never inferred from the Blob.
type ImportContext struct {
	OriginalFilename   string
	RelativePath       string
	FullPath           string
	FilesystemCreated  *time.Time
	FilesystemModified *time.Time
}

type Memory struct {
	ID            uuid.UUID
	BlobRef       Blobref
	ImportContext ImportContext
	MediaType     string
	ByteSize      int64
	ImportedAt    time.Time
}

// ListCursor identifies the last Memory observed by a caller. It is kept
// transport-neutral; HTTP encoding remains the adapter's responsibility.
type ListCursor struct {
	ImportedAt time.Time
	ID         uuid.UUID
}

type DuplicateError struct {
	Existing Memory
}

func (e *DuplicateError) Error() string {
	return "Duplicate Blob already belongs to Memory " + e.Existing.ID.String()
}

type Vault struct {
	db        *sql.DB
	blobDir   string
	uploadDir string
}

func Open(ctx context.Context, databasePath, blobDir, uploadDir string) (*Vault, error) {
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	contentDir := filepath.Join(blobDir, "sha256")
	if err := os.MkdirAll(contentDir, 0o755); err != nil {
		return nil, fmt.Errorf("create Blob directory: %w", err)
	}
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		return nil, fmt.Errorf("create Blob upload directory: %w", err)
	}
	if err := removeAbandonedTemporaryFiles(uploadDir, contentDir); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return nil, fmt.Errorf("open Vault database: %w", err)
	}
	db.SetMaxOpenConns(maxOpenConnections)
	v := &Vault{
		db:        db,
		blobDir:   contentDir,
		uploadDir: uploadDir,
	}
	if err := v.initialize(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return v, nil
}

func (v *Vault) initialize(ctx context.Context) error {
	if _, err := v.db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("enable Vault foreign keys: %w", err)
	}

	const schema = `CREATE TABLE IF NOT EXISTS memories (
		id TEXT PRIMARY KEY,
		blob_hash TEXT NOT NULL UNIQUE CHECK (
			length(blob_hash) = 71 AND
			substr(blob_hash, 1, 7) = 'sha256-' AND
			substr(blob_hash, 8) NOT GLOB '*[^0-9a-f]*'
		),
		original_filename TEXT NOT NULL,
		relative_path TEXT,
		full_path TEXT,
		filesystem_created_at TEXT,
		filesystem_modified_at TEXT,
		media_type TEXT NOT NULL,
		byte_size INTEGER NOT NULL,
		imported_at TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS understanding_runs (
		id TEXT PRIMARY KEY,
		memory_id TEXT NOT NULL,
		pipeline TEXT NOT NULL,
		created_at TEXT NOT NULL,
		completed_at TEXT NOT NULL,
		extractor_versions_json TEXT NOT NULL DEFAULT '{}'
			CHECK (
				json_valid(extractor_versions_json) AND
				json_type(extractor_versions_json) = 'object'
			),
		warnings_json TEXT NOT NULL DEFAULT '[]'
			CHECK (
				json_valid(warnings_json) AND
				json_type(warnings_json) = 'array'
			),
		UNIQUE (id, memory_id),
		FOREIGN KEY (memory_id) REFERENCES memories(id) ON DELETE CASCADE
	);
	CREATE TABLE IF NOT EXISTS active_understanding_runs (
		memory_id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL UNIQUE,
		FOREIGN KEY (memory_id) REFERENCES memories(id) ON DELETE CASCADE,
		FOREIGN KEY (run_id, memory_id)
			REFERENCES understanding_runs(id, memory_id) ON DELETE CASCADE
	)`
	if _, err := v.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("initialize Vault database: %w", err)
	}
	rows, err := v.db.QueryContext(ctx, `SELECT blob_hash FROM memories`)
	if err != nil {
		return fmt.Errorf("validate stored Blobrefs: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			logging.FromContext(ctx).WarnContext(
				ctx,
				"Could not close stored Blobref rows",
				"error", err,
			)
		}
	}()
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return fmt.Errorf("read stored Blobref: %w", err)
		}
		if _, err := ParseBlobref(value); err != nil {
			return fmt.Errorf("invalid stored Blobref %q: %w", value, err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("validate stored Blobrefs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close stored Blobref rows: %w", err)
	}
	if err := v.validateSchema(ctx); err != nil {
		return err
	}
	return nil
}

func (v *Vault) validateSchema(ctx context.Context) error {
	wantColumns := map[string][]string{
		"memories": {
			"id",
			"blob_hash",
			"original_filename",
			"relative_path",
			"full_path",
			"filesystem_created_at",
			"filesystem_modified_at",
			"media_type",
			"byte_size",
			"imported_at",
		},
		"understanding_runs": {
			"id",
			"memory_id",
			"pipeline",
			"created_at",
			"completed_at",
			"extractor_versions_json",
			"warnings_json",
		},
		"active_understanding_runs": {"memory_id", "run_id"},
	}
	for table, want := range wantColumns {
		got, err := tableColumns(ctx, v.db, table)
		if err != nil {
			return err
		}
		if !slices.Equal(got, want) {
			return fmt.Errorf(
				"vault schema is incompatible: table %s has columns %v; "+
					"recreate the database and reimport content",
				table,
				got,
			)
		}
	}
	return nil
}

func tableColumns(ctx context.Context, database *sql.DB, table string) ([]string, error) {
	rows, err := database.QueryContext(
		ctx,
		`SELECT name FROM pragma_table_info(?) ORDER BY cid`,
		table,
	)
	if err != nil {
		return nil, fmt.Errorf("inspect %s schema: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	var columns []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			return nil, fmt.Errorf("read %s schema: %w", table, err)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read %s schema: %w", table, err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close %s schema rows: %w", table, err)
	}
	return columns, nil
}

func (v *Vault) Close() error {
	return v.db.Close()
}

func (v *Vault) Put(ctx context.Context, candidate Import) (Memory, error) {
	if candidate.Content == nil {
		return Memory{}, errors.New("blob content is required")
	}
	importContext, err := candidate.Context.normalized()
	if err != nil {
		return Memory{}, err
	}
	logger := logging.FromContext(ctx)
	logger.DebugContext(
		ctx, "Blob import staging started", "original_filename", importContext.OriginalFilename,
	)

	temporary, err := os.CreateTemp(v.uploadDir, ".import-*")
	if err != nil {
		return Memory{}, fmt.Errorf("create temporary Blob: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		if err := os.Remove(temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			logger.WarnContext(ctx, "Could not remove temporary Blob", "error", err)
		}
	}()

	hash := sha256.New()
	byteSize, copyErr := copyWithLimit(
		io.MultiWriter(temporary, hash), candidate.Content, MaxBlobBytes,
	)
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if copyErr != nil {
		return Memory{}, copyErr
	}
	if syncErr != nil {
		return Memory{}, fmt.Errorf("flush temporary Blob: %w", syncErr)
	}
	if closeErr != nil {
		return Memory{}, fmt.Errorf("close temporary Blob: %w", closeErr)
	}

	mediaType, err := resolveMediaType(temporaryPath, candidate.DeclaredMediaType)
	if err != nil {
		return Memory{}, err
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	blobRef := NewSHA256Blobref(digest)
	logger.DebugContext(ctx, "Blob import staged",
		"blob_hash", blobRef.String(),
		"byte_size", byteSize,
		"media_type", mediaType,
		"temporary_path", temporaryPath,
	)
	finalPath := v.blobPath(blobRef)
	if err := v.ensureBlobDirectory(finalPath); err != nil {
		return Memory{}, fmt.Errorf("create Blob shard directory: %w", err)
	}
	if err := publishBlob(temporaryPath, finalPath, blobRef, byteSize); err != nil {
		return Memory{}, fmt.Errorf("publish Blob: %w", err)
	}
	logger.DebugContext(ctx, "Blob published or verified", "blob_hash", blobRef.String())

	now := time.Now().UTC()
	memory := Memory{
		ID:            uuid.New(),
		BlobRef:       blobRef,
		ImportContext: importContext,
		MediaType:     mediaType,
		ByteSize:      byteSize,
		ImportedAt:    now,
	}

	// The Blob is durable now. The Memory commit must survive client
	// disconnection so successful storage is never reported as a failed import.
	insertSQL := `INSERT INTO memories (
		id, blob_hash, original_filename, relative_path, full_path,
		filesystem_created_at, filesystem_modified_at, media_type, byte_size,
		imported_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(blob_hash) DO NOTHING`

	durableContext := context.WithoutCancel(ctx)
	result, err := v.db.ExecContext(
		durableContext,
		insertSQL,
		memory.ID.String(),
		memory.BlobRef.String(),
		memory.ImportContext.OriginalFilename,
		nullableString(memory.ImportContext.RelativePath),
		nullableString(memory.ImportContext.FullPath),
		nullableTime(memory.ImportContext.FilesystemCreated),
		nullableTime(memory.ImportContext.FilesystemModified),
		memory.MediaType,
		memory.ByteSize,
		memory.ImportedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return Memory{}, fmt.Errorf("commit Memory: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return Memory{}, fmt.Errorf("inspect Memory commit: %w", err)
	}
	if inserted == 0 {
		existing, err := v.memoryByHash(durableContext, blobRef)
		if err != nil {
			return Memory{}, err
		}
		logger.InfoContext(durableContext, "Duplicate import rejected",
			"memory_id", existing.ID,
			"blob_hash", existing.BlobRef.String(),
		)
		return Memory{}, &DuplicateError{Existing: existing}
	}

	logger.InfoContext(durableContext, "Memory imported",
		"memory_id", memory.ID,
		"blob_hash", memory.BlobRef.String(),
		"byte_size", memory.ByteSize,
		"media_type", memory.MediaType,
	)
	return memory, nil
}

func (v *Vault) Memory(ctx context.Context, id uuid.UUID) (Memory, error) {
	return scanMemory(
		v.db.QueryRowContext(ctx, selectMemory+` WHERE id = ?`, id.String()),
	)
}

func (v *Vault) ListMemories(
	ctx context.Context,
	limit int,
	after *ListCursor,
) ([]Memory, bool, error) {
	if limit <= 0 || limit > MaxListLimit {
		limit = DefaultListLimit
	}
	query := selectMemory
	arguments := []any{}
	if after != nil {
		query += ` WHERE julianday(imported_at) < julianday(?) OR ` +
			`(julianday(imported_at) = julianday(?) AND id < ?)`
		encodedTime := after.ImportedAt.Format(time.RFC3339Nano)
		arguments = append(
			arguments,
			encodedTime,
			encodedTime,
			after.ID.String(),
		)
	}
	query += ` ORDER BY julianday(imported_at) DESC, id DESC LIMIT ?`
	arguments = append(arguments, limit+1)
	rows, err := v.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, false, fmt.Errorf("list Memories: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			logging.FromContext(ctx).WarnContext(ctx, "Could not close Memory rows", "error", err)
		}
	}()
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

func (v *Vault) OpenContent(
	ctx context.Context,
	id uuid.UUID,
) (Memory, io.ReadCloser, error) {
	logger := logging.FromContext(ctx)
	logger.DebugContext(ctx, "Opening Memory Blob", "memory_id", id)
	memory, err := v.Memory(ctx, id)
	if err != nil {
		return Memory{}, nil, err
	}
	blobPath := v.blobPath(memory.BlobRef)
	if err := verifyBlob(blobPath, memory.BlobRef, memory.ByteSize); err != nil {
		return Memory{}, nil, err
	}
	content, err := os.Open(blobPath)
	if err != nil {
		return Memory{}, nil, fmt.Errorf("%w: open %s: %v", ErrBlobUnavailable, memory.BlobRef, err)
	}
	logger.DebugContext(ctx, "Memory Blob opened",
		"memory_id", memory.ID,
		"blob_hash", memory.BlobRef.String(),
		"byte_size", memory.ByteSize,
		"media_type", memory.MediaType,
	)
	return memory, content, nil
}

// publishBlob copies into the content-addressed filesystem and uses a hard
// link for atomic no-replace publication. This has identical semantics when
// upload staging and content storage are on different filesystems.
func publishBlob(sourcePath, finalPath string, ref Blobref, expectedSize int64) error {
	if _, err := os.Stat(finalPath); err == nil {
		return verifyBlob(finalPath, ref, expectedSize)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect Blob destination: %w", err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open staged Blob: %w", err)
	}
	staged, err := os.CreateTemp(filepath.Dir(finalPath), ".publish-*")
	if err != nil {
		_ = source.Close()
		return fmt.Errorf("create publishing Blob: %w", err)
	}
	stagedPath := staged.Name()
	defer func() { _ = os.Remove(stagedPath) }()

	written, copyErr := io.Copy(staged, source)
	sourceCloseErr := source.Close()
	syncErr := staged.Sync()
	stagedCloseErr := staged.Close()
	if copyErr != nil {
		return fmt.Errorf("copy staged Blob: %w", copyErr)
	}
	if sourceCloseErr != nil {
		return fmt.Errorf("close staged Blob: %w", sourceCloseErr)
	}
	if syncErr != nil {
		return fmt.Errorf("flush publishing Blob: %w", syncErr)
	}
	if stagedCloseErr != nil {
		return fmt.Errorf("close publishing Blob: %w", stagedCloseErr)
	}
	if written != expectedSize {
		return fmt.Errorf("copy staged Blob: wrote %d bytes, expected %d", written, expectedSize)
	}
	if err := verifyBlob(stagedPath, ref, expectedSize); err != nil {
		return err
	}
	if err := os.Link(stagedPath, finalPath); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return verifyBlob(finalPath, ref, expectedSize)
		}
		return fmt.Errorf("atomically publish Blob: %w", err)
	}
	if err := syncDirectory(filepath.Dir(finalPath)); err != nil {
		return fmt.Errorf("flush Blob directory: %w", err)
	}
	return nil
}

func verifyBlob(path string, ref Blobref, expectedSize int64) error {
	content, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%w: open %s: %v", ErrBlobUnavailable, ref, err)
	}
	hash := sha256.New()
	actualSize, copyErr := io.Copy(hash, content)
	closeErr := content.Close()
	if copyErr != nil {
		return fmt.Errorf("%w: read %s: %v", ErrBlobUnavailable, ref, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("%w: close %s: %v", ErrBlobUnavailable, ref, closeErr)
	}
	actualRef, err := ParseBlobref("sha256-" + fmt.Sprintf("%x", hash.Sum(nil)))
	if err != nil {
		return fmt.Errorf("calculate Blobref: %w", err)
	}
	if actualSize != expectedSize || actualRef != ref {
		return fmt.Errorf(
			"%w: %s has size %d and digest %s; expected size %d and digest %s",
			ErrBlobUnavailable,
			ref,
			actualSize,
			actualRef,
			expectedSize,
			ref,
		)
	}
	return nil
}

func (v *Vault) ensureBlobDirectory(finalPath string) error {
	directory := filepath.Dir(finalPath)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	for _, path := range []string{
		v.blobDir,
		filepath.Join(v.blobDir, filepath.Base(filepath.Dir(directory))),
		directory,
	} {
		if err := syncDirectory(path); err != nil {
			return err
		}
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func removeAbandonedTemporaryFiles(uploadDir, contentDir string) error {
	entries, err := os.ReadDir(uploadDir)
	if err != nil {
		return fmt.Errorf("scan Blob upload directory: %w", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".import-") {
			if err := os.Remove(filepath.Join(uploadDir, entry.Name())); err != nil {
				return fmt.Errorf("remove abandoned import staging: %w", err)
			}
		}
	}
	return filepath.WalkDir(contentDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), ".publish-") {
			return nil
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove abandoned publication staging: %w", err)
		}
		return nil
	})
}

func (v *Vault) blobPath(ref Blobref) string {
	digest := ref.digestHex()
	return filepath.Join(
		v.blobDir,
		digest[:firstShardEnd],
		digest[firstShardEnd:secondShardEnd],
		ref.String(),
	)
}

const selectMemory = `SELECT id, blob_hash, original_filename, relative_path, full_path,
	filesystem_created_at, filesystem_modified_at, media_type, byte_size, imported_at
	FROM memories`

type rowScanner interface{ Scan(...any) error }

func scanMemory(row rowScanner) (Memory, error) {
	var memory Memory
	var id, importedAt, blobHash string
	var relativePath, fullPath, createdAt, modifiedAt sql.NullString
	err := row.Scan(
		&id,
		&blobHash,
		&memory.ImportContext.OriginalFilename,
		&relativePath,
		&fullPath,
		&createdAt,
		&modifiedAt,
		&memory.MediaType,
		&memory.ByteSize,
		&importedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Memory{}, ErrMemoryNotFound
	}
	if err != nil {
		return Memory{}, fmt.Errorf("read Memory: %w", err)
	}
	memory.BlobRef, err = ParseBlobref(blobHash)
	if err != nil {
		return Memory{}, fmt.Errorf("read Memory Blobref: %w", err)
	}
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return Memory{}, fmt.Errorf("read Memory ID: %w", err)
	}
	memory.ID = parsedID
	memory.ImportContext.RelativePath = relativePath.String
	memory.ImportContext.FullPath = fullPath.String
	memory.ImportedAt, err = time.Parse(time.RFC3339Nano, importedAt)
	if err != nil {
		return Memory{}, fmt.Errorf("read import time: %w", err)
	}
	if memory.ImportContext.FilesystemCreated, err = parseNullableTime(
		createdAt,
	); err != nil {
		return Memory{}, err
	}
	if memory.ImportContext.FilesystemModified, err = parseNullableTime(
		modifiedAt,
	); err != nil {
		return Memory{}, err
	}
	return memory, nil
}

func (v *Vault) memoryByHash(ctx context.Context, ref Blobref) (Memory, error) {
	return scanMemory(
		v.db.QueryRowContext(ctx, selectMemory+` WHERE blob_hash = ?`, ref.String()),
	)
}

func copyWithLimit(
	destination io.Writer,
	source io.Reader,
	limit int64,
) (int64, error) {
	written, err := io.Copy(destination, io.LimitReader(source, limit))
	if err != nil {
		return written, fmt.Errorf("copy Blob: %w", err)
	}
	if written < limit {
		return written, nil
	}
	var extra [overflowProbeBytes]byte
	n, err := source.Read(extra[:])
	if err != nil && !errors.Is(err, io.EOF) {
		return written, fmt.Errorf("check Blob size: %w", err)
	}
	if n != 0 {
		return written, ErrBlobTooLarge
	}
	return written, nil
}

func safeFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < asciiControlLimit || r == asciiDelete {
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
