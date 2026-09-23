package vault

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anton-povarov/memoryd/server/internal/logging"
)

func TestResolveMediaType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		content  []byte
		declared string
		want     string
		invalid  bool
	}{
		{
			name:     "detected PDF overrides conflicting declaration",
			content:  []byte("%PDF-1.7\n"),
			declared: "image/png",
			want:     "application/pdf",
			invalid:  false,
		},
		{
			name:     "detected PDF ignores malformed declaration",
			content:  []byte("%PDF-1.7\n"),
			declared: "nonsense",
			want:     "application/pdf",
			invalid:  false,
		},
		{
			name:     "library detects JSON beyond old format list",
			content:  []byte(`{"answer":42}`),
			declared: "application/octet-stream",
			want:     "application/json",
			invalid:  false,
		},
		{
			name:     "specific Markdown declaration refines plain text",
			content:  []byte("# Notes\nA café visit.\n"),
			declared: "Text/Markdown; charset=UTF-8",
			want:     "text/markdown",
			invalid:  false,
		},
		{
			name:     "generic declaration keeps detected plain text",
			content:  []byte("Ordinary prose.\n"),
			declared: "application/octet-stream",
			want:     "text/plain",
			invalid:  false,
		},
		{
			name:     "unknown bytes use normalized client type",
			content:  []byte{0x00, 0x01, 0x02, 0xff},
			declared: "Application/X-Custom; version=1",
			want:     "application/x-custom",
			invalid:  false,
		},
		{
			name:     "unknown bytes use octet stream without declaration",
			content:  []byte{0x00, 0x01, 0x02, 0xff},
			declared: "",
			want:     "application/octet-stream",
			invalid:  false,
		},
		{
			name:     "malformed fallback declaration is rejected",
			content:  []byte{0x00, 0x01, 0x02, 0xff},
			declared: "nonsense",
			want:     "",
			invalid:  true,
		},
		{
			name:     "media range is not a concrete fallback type",
			content:  []byte{0x00, 0x01, 0x02, 0xff},
			declared: "*/*",
			want:     "",
			invalid:  true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "candidate")
			if err := os.WriteFile(path, test.content, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := resolveMediaType(path, test.declared)
			if test.invalid {
				if !errors.Is(err, ErrInvalidMediaType) {
					t.Fatalf("resolveMediaType() error = %v, want invalid media type", err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("resolveMediaType() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func openVault(ctx context.Context, root string) (*Vault, error) {
	logger := logging.NewRoot(slog.LevelInfo, io.Discard)
	databasePath := filepath.Join(root, "memoryd.sqlite")
	blobDir := filepath.Join(root, "blobs")
	uploadDir := filepath.Join(root, "uploads")
	return Open(ctx, logger, databasePath, blobDir, uploadDir)
}

func TestVaultPutOpenContentPersistsAndRejectsDuplicate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir() // reused a couple times
	wantBytes := []byte("%PDF-1.7\nbyte-exact memory\n%%EOF\n")
	modifiedAt := time.Date(2026, 9, 15, 10, 11, 12, 0, time.UTC)

	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	created, err := v.Put(ctx, Import{
		Content:           bytes.NewReader(wantBytes),
		DeclaredMediaType: "",
		Context: ImportContext{
			OriginalFilename:   "notes.pdf",
			RelativePath:       "",
			FullPath:           "/original/notes.pdf",
			FilesystemCreated:  nil,
			FilesystemModified: &modifiedAt,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.MediaType != "application/pdf" {
		t.Fatalf("created Memory = %#v", created)
	}
	wantBlobref := NewSHA256Blobref(sha256.Sum256(wantBytes))
	if created.BlobRef != wantBlobref {
		t.Fatalf("Blobref = %s, want %s", created.BlobRef, wantBlobref)
	}
	var storedRef string
	if err := v.db.QueryRowContext(
		ctx,
		`SELECT blob_hash FROM memories WHERE id = ?`,
		created.ID.String(),
	).Scan(&storedRef); err != nil {
		t.Fatal(err)
	}
	if storedRef != created.BlobRef.String() {
		t.Fatalf("stored Blobref = %q, want %q", storedRef, created.BlobRef)
	}
	if _, err := v.db.ExecContext(
		ctx,
		`UPDATE memories SET blob_hash = ? WHERE id = ?`,
		created.BlobRef.digestHex(),
		created.ID.String(),
	); err == nil {
		t.Fatal("database accepted an unprefixed digest")
	}
	blobPath := v.blobPath(wantBlobref)
	storedBytes, err := os.ReadFile(blobPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedBytes, wantBytes) {
		t.Fatalf("stored content = %q, want %q", storedBytes, wantBytes)
	}
	stagedEntries, err := os.ReadDir(v.uploadDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(stagedEntries) != 0 {
		t.Fatalf("upload directory contains %d staged files", len(stagedEntries))
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}

	v, err = openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })

	opened, content, err := v.OpenContent(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer content.Close()
	gotBytes, err := io.ReadAll(content)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotBytes, wantBytes) {
		t.Fatalf("content = %q, want %q", gotBytes, wantBytes)
	}
	if opened.ID != created.ID || opened.BlobRef != created.BlobRef ||
		opened.ImportContext.OriginalFilename != "notes.pdf" {
		t.Fatalf("reopened Memory = %#v, created = %#v", opened, created)
	}
	if opened.ImportContext.FullPath != "/original/notes.pdf" ||
		opened.ImportContext.FilesystemCreated != nil ||
		opened.ImportContext.FilesystemModified == nil ||
		!opened.ImportContext.FilesystemModified.Equal(modifiedAt) {
		t.Fatalf("reopened Import Context = %#v", opened.ImportContext)
	}
	if facts := opened.ImportContext.Facts(); len(facts) != 3 {
		t.Fatalf("reopened provenance Facts = %#v", facts)
	}

	_, err = v.Put(ctx, Import{
		Content:           bytes.NewReader(wantBytes),
		DeclaredMediaType: "",
		Context: ImportContext{
			OriginalFilename:   "renamed.pdf",
			RelativePath:       "",
			FullPath:           "",
			FilesystemCreated:  nil,
			FilesystemModified: nil,
		},
	})
	var duplicate *DuplicateError
	if !errors.As(err, &duplicate) {
		t.Fatalf("duplicate error = %v, want *DuplicateError", err)
	}
	if duplicate.Existing.ID != created.ID {
		t.Fatalf(
			"duplicate Memory ID = %s, want %s",
			duplicate.Existing.ID,
			created.ID,
		)
	}
	if duplicate.Existing.ImportContext.OriginalFilename != "notes.pdf" {
		t.Fatalf("Duplicate changed Import Context: %#v", duplicate.Existing.ImportContext)
	}
}

func TestVaultDeleteRemovesMemoryRunsAndBlob(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })

	wantBytes := []byte("%PDF-1.7\ndelete me\n%%EOF\n")
	memory, err := v.Put(ctx, Import{
		Content:           bytes.NewReader(wantBytes),
		DeclaredMediaType: "",
		Context: ImportContext{
			OriginalFilename:   "remove.pdf",
			RelativePath:       "",
			FullPath:           "",
			FilesystemCreated:  nil,
			FilesystemModified: nil,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A committed Understanding Run must cascade with its Memory.
	_, err = v.db.ExecContext(
		ctx,
		`INSERT INTO understanding_runs (
			id, memory_id, pipeline, created_at, completed_at
		) VALUES (?, ?, ?, ?, ?)`,
		"run",
		memory.ID.String(),
		"regular",
		time.Now().UTC().Format(time.RFC3339Nano),
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		t.Fatal(err)
	}
	blobPath := v.blobPath(memory.BlobRef)
	if _, err := os.Stat(blobPath); err != nil {
		t.Fatal(err)
	}

	if err := v.Delete(ctx, memory.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Memory(ctx, memory.ID); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("Memory lookup after delete = %v, want ErrMemoryNotFound", err)
	}
	if _, err := os.Stat(blobPath); err == nil {
		t.Fatal("Blob file remains after Memory deletion")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspect removed Blob: %v", err)
	}
	var runCount int
	if err := v.db.QueryRowContext(
		ctx,
		`SELECT count(*) FROM understanding_runs WHERE memory_id = ?`,
		memory.ID.String(),
	).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 0 {
		t.Fatalf("Understanding Runs remain after Memory deletion: %d", runCount)
	}

	if err := v.Delete(ctx, memory.ID); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("second delete = %v, want ErrMemoryNotFound", err)
	}
}

func TestCopyWithLimitRejectsFirstByteOverLimit(t *testing.T) {
	t.Parallel()

	var destination bytes.Buffer
	written, err := copyWithLimit(
		&destination,
		bytes.NewReader([]byte("123456")),
		5,
	)
	if !errors.Is(err, ErrBlobTooLarge) {
		t.Fatalf("error = %v, want ErrBlobTooLarge", err)
	}
	if written != 5 || destination.String() != "12345" {
		t.Fatalf("written = %d, content = %q", written, destination.String())
	}
}

func TestVaultPutAcceptsExactlyOneHundredMiB(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })

	memory, err := v.Put(ctx, Import{
		Content: io.LimitReader(repeatedByteReader{'x'}, MaxBlobBytes),
		Context: ImportContext{
			OriginalFilename:   "maximum.bin",
			RelativePath:       "",
			FullPath:           "",
			FilesystemCreated:  nil,
			FilesystemModified: nil,
		},
		DeclaredMediaType: "application/octet-stream",
	})
	if err != nil {
		t.Fatal(err)
	}
	if memory.ByteSize != MaxBlobBytes {
		t.Fatalf("ByteSize = %d, want %d", memory.ByteSize, MaxBlobBytes)
	}
}

type repeatedByteReader struct {
	value byte
}

func (reader repeatedByteReader) Read(destination []byte) (int, error) {
	for index := range destination {
		destination[index] = reader.value
	}
	return len(destination), nil
}

func TestOpenRejectsInvalidStoredBlobref(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "memoryd.sqlite")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `CREATE TABLE memories (
		id TEXT PRIMARY KEY, blob_hash TEXT NOT NULL UNIQUE
	)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(
		ctx,
		`INSERT INTO memories (id, blob_hash) VALUES (?, ?)`,
		"legacy", strings.Repeat("a", sha256.Size*2),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = openVault(ctx, root)
	if err == nil {
		t.Fatal("expected invalid stored Blobref error")
	}
	if !strings.Contains(err.Error(), "invalid stored Blobref") {
		t.Fatalf("unexpected error = %v", err)
	}
}

func TestOpenCreatesSeparatedMemoryAndUnderstandingSchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })

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
		got := readTableColumnsForTest(t, v.db, table)
		if !slices.Equal(got, want) {
			t.Errorf("%s columns = %v, want %v", table, got, want)
		}
	}

	_, err = v.db.ExecContext(
		ctx,
		`INSERT INTO understanding_runs (
			id, memory_id, pipeline, created_at, completed_at
		) VALUES (?, ?, ?, ?, ?)`,
		"run",
		"missing-memory",
		"regular",
		time.Now().UTC().Format(time.RFC3339Nano),
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Fatalf("orphan Understanding Run insert error = %v", err)
	}
}

func TestOpenRejectsLegacyMixedMemorySchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "memoryd.sqlite")
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.ExecContext(ctx, `CREATE TABLE memories (
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
	)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = openVault(ctx, root)
	if err == nil || !strings.Contains(err.Error(), "recreate the database and reimport") {
		t.Fatalf("Open() error = %v", err)
	}
}

func readTableColumnsForTest(t *testing.T, database *sql.DB, table string) []string {
	t.Helper()
	rows, err := database.QueryContext(
		t.Context(),
		`SELECT name FROM pragma_table_info(?) ORDER BY cid`,
		table,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Error(err)
		}
	}()
	var columns []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return columns
}

func TestPutRejectsCorruptExistingBlob(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })

	content := []byte("expected content")
	digest := sha256.Sum256(content)
	ref := NewSHA256Blobref(digest)
	path := v.blobPath(ref)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupted bytes!"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = v.Put(ctx, Import{
		Content: bytes.NewReader(content),
		Context: ImportContext{
			OriginalFilename:   "memory.bin",
			RelativePath:       "",
			FullPath:           "",
			FilesystemCreated:  nil,
			FilesystemModified: nil,
		},
		DeclaredMediaType: "application/octet-stream",
	})
	if !errors.Is(err, ErrBlobUnavailable) {
		t.Fatalf("Put() error = %v, want ErrBlobUnavailable", err)
	}
	memories, _, listErr := v.ListMemories(ctx, DefaultListLimit, nil)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(memories) != 0 {
		t.Fatalf("committed Memories = %d, want 0", len(memories))
	}
}

func TestConcurrentIdenticalImportsPublishOneBlobAndMemory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })

	content := []byte("same immutable bytes")
	const importers = 8
	errorsFromImports := make(chan error, importers)
	var waitGroup sync.WaitGroup
	for index := 0; index < importers; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, err := v.Put(ctx, Import{
				Content: bytes.NewReader(content),
				Context: ImportContext{
					OriginalFilename:   "same.txt",
					RelativePath:       "",
					FullPath:           "",
					FilesystemCreated:  nil,
					FilesystemModified: nil,
				},
				DeclaredMediaType: "text/plain",
			})
			errorsFromImports <- err
		}()
	}
	waitGroup.Wait()
	close(errorsFromImports)

	succeeded := 0
	duplicates := 0
	for err := range errorsFromImports {
		var duplicate *DuplicateError
		switch {
		case err == nil:
			succeeded++
		case errors.As(err, &duplicate):
			duplicates++
		default:
			t.Fatalf("concurrent Put() error = %v", err)
		}
	}
	if succeeded != 1 || duplicates != importers-1 {
		t.Fatalf("successful=%d duplicate=%d", succeeded, duplicates)
	}

	digest := sha256.Sum256(content)
	ref := NewSHA256Blobref(digest)
	if err := verifyBlob(v.blobPath(ref), ref, int64(len(content))); err != nil {
		t.Fatal(err)
	}
}

func TestStartupCleansStagingAndPreservesPublishedOrphan(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	uploadDir := filepath.Join(root, "uploads")
	contentDir := filepath.Join(root, "blobs", "sha256")
	orphanBytes := []byte("published orphan")
	orphanRef := NewSHA256Blobref(sha256.Sum256(orphanBytes))
	stagingVault := Vault{
		db:        nil,
		blobDir:   contentDir,
		uploadDir: uploadDir,
	}
	orphan := stagingVault.blobPath(orphanRef)
	shardDir := filepath.Dir(orphan)
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(shardDir, 0o755); err != nil {
		t.Fatal(err)
	}
	importTemporary := filepath.Join(uploadDir, ".import-abandoned")
	publishTemporary := filepath.Join(shardDir, ".publish-abandoned")
	for _, path := range []string{importTemporary, publishTemporary} {
		if err := os.WriteFile(path, []byte("bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(orphan, orphanBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	for _, path := range []string{importTemporary, publishTemporary} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("temporary path %s still exists: %v", path, err)
		}
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("published Orphan Blob was removed: %v", err)
	}
}

func TestOpenContentDiagnosesMissingAndCorruptBlob(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	content := []byte("original")
	memory, err := v.Put(ctx, Import{
		Content: bytes.NewReader(content),
		Context: ImportContext{
			OriginalFilename:   "memory.txt",
			RelativePath:       "",
			FullPath:           "",
			FilesystemCreated:  nil,
			FilesystemModified: nil,
		},
		DeclaredMediaType: "text/plain",
	})
	if err != nil {
		t.Fatal(err)
	}
	path := v.blobPath(memory.BlobRef)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := v.OpenContent(ctx, memory.ID); !errors.Is(err, ErrBlobUnavailable) {
		t.Fatalf("missing Blob error = %v", err)
	}
	if err := os.WriteFile(path, []byte("changed!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := v.OpenContent(ctx, memory.ID); !errors.Is(err, ErrBlobUnavailable) {
		t.Fatalf("corrupt Blob error = %v", err)
	}
}

func TestInterruptedCommitLeavesPublishedOrphanForRestart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	content := []byte("published before failed commit")
	_, err = v.Put(ctx, Import{
		Content: bytes.NewReader(content),
		Context: ImportContext{
			OriginalFilename:   "orphan.txt",
			RelativePath:       "",
			FullPath:           "",
			FilesystemCreated:  nil,
			FilesystemModified: nil,
		},
		DeclaredMediaType: "text/plain",
	})
	if err == nil || !strings.Contains(err.Error(), "commit Memory") {
		t.Fatalf("Put() error = %v, want failed Memory commit", err)
	}

	digest := sha256.Sum256(content)
	ref := NewSHA256Blobref(digest)
	orphanPath := v.blobPath(ref)
	reopened, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if err := verifyBlob(orphanPath, ref, int64(len(content))); err != nil {
		t.Fatalf("published Orphan Blob did not survive restart: %v", err)
	}
	memories, _, err := reopened.ListMemories(ctx, DefaultListLimit, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 0 {
		t.Fatalf("failed commit left %d Memories", len(memories))
	}
}
