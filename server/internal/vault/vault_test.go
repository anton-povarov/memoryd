package vault

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVaultPutOpenContentPersistsAndRejectsDuplicate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "memoryd.sqlite")
	blobDir := filepath.Join(root, "blobs")
	uploadDir := filepath.Join(root, "uploads")
	wantBytes := []byte("%PDF-1.7\nbyte-exact memory\n%%EOF\n")
	modifiedAt := time.Date(2026, 9, 15, 10, 11, 12, 0, time.UTC)

	v, err := Open(ctx, databasePath, blobDir, uploadDir)
	if err != nil {
		t.Fatal(err)
	}
	created, err := v.Put(ctx, Import{
		Content:            bytes.NewReader(wantBytes),
		OriginalFilename:   "notes.pdf",
		RelativePath:       "",
		FullPath:           "/original/notes.pdf",
		FilesystemCreated:  nil,
		FilesystemModified: &modifiedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.UnderstandingState != StateDone ||
		created.MediaType != "application/pdf" {
		t.Fatalf("created Memory = %#v", created)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(wantBytes))
	if created.BlobRef.String() != "sha256-"+digest {
		t.Fatalf("Blobref = %s, want sha256-%s", created.BlobRef, digest)
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
	blobPath := filepath.Join(
		blobDir,
		"sha256",
		digest[:2],
		digest[2:4],
		"sha256-"+digest,
	)
	storedBytes, err := os.ReadFile(blobPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedBytes, wantBytes) {
		t.Fatalf("stored content = %q, want %q", storedBytes, wantBytes)
	}
	stagedEntries, err := os.ReadDir(uploadDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(stagedEntries) != 0 {
		t.Fatalf("upload directory contains %d staged files", len(stagedEntries))
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}

	v, err = Open(ctx, databasePath, blobDir, uploadDir)
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
		opened.OriginalFilename != "notes.pdf" {
		t.Fatalf("reopened Memory = %#v, created = %#v", opened, created)
	}

	_, err = v.Put(ctx, Import{
		Content: bytes.NewReader(wantBytes), OriginalFilename: "renamed.pdf",
		RelativePath: "", FullPath: "",
		FilesystemCreated: nil, FilesystemModified: nil,
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
	_, err = Open(
		ctx,
		databasePath,
		filepath.Join(root, "blobs"),
		filepath.Join(root, "uploads"),
	)
	if err == nil {
		t.Fatal("expected invalid stored Blobref error")
	}
	if !strings.Contains(err.Error(), "invalid stored Blobref") {
		t.Fatalf("unexpected error = %v", err)
	}
}
