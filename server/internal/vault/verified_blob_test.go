package vault

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadVerifiedBlob(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blob")
	content := []byte("plain content")
	ref := NewSHA256Blobref(sha256.Sum256(content))
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := readVerifiedBlob(path, ref, int64(len(content)))
	if err != nil || got != string(content) {
		t.Fatalf("readVerifiedBlob() = %q, %v; want %q, nil", got, err, content)
	}

	if _, err := readVerifiedBlob(
		filepath.Join(dir, "missing"),
		ref,
		int64(len(content)),
	); !errors.Is(
		err,
		ErrBlobUnavailable,
	) {
		t.Fatalf("missing Blob error = %v, want ErrBlobUnavailable", err)
	}

	if err := os.WriteFile(path, []byte("other content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readVerifiedBlob(
		path,
		ref,
		int64(len(content)),
	); !errors.Is(
		err,
		ErrBlobUnavailable,
	) {
		t.Fatalf("corrupt Blob error = %v, want ErrBlobUnavailable", err)
	}
}
