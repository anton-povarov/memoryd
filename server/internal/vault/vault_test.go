package vault

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVaultPutOpenContentPersistsAndRejectsDuplicate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "memoryd.sqlite")
	blobDir := filepath.Join(root, "blobs")
	wantBytes := []byte("%PDF-1.7\nbyte-exact memory\n%%EOF\n")
	modifiedAt := time.Date(2026, 9, 15, 10, 11, 12, 0, time.UTC)

	v, err := Open(ctx, databasePath, blobDir)
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
	if created.BlobHash != digest {
		t.Fatalf("Blob hash = %s, want %s", created.BlobHash, digest)
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
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}

	v, err = Open(ctx, databasePath, blobDir)
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
	if opened.ID != created.ID || opened.BlobHash != created.BlobHash ||
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
