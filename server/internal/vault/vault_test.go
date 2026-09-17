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
