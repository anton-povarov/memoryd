package vault

import (
	"context"
	"os"
	"testing"
)

func TestSearchUpgradesLegacyProjectionWithoutOriginalReread(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	memory := putSearchFixture(
		t,
		ctx,
		v,
		"passport Anton retained projection",
		ImportContext{OriginalFilename: "opaque.txt"},
	)
	// Emulate ticket 01b's compatible derived storage, not authoritative schemas.
	for _, statement := range []string{
		`CREATE TEMP TABLE old_projection AS SELECT rowid, filename, paths, body, note FROM memory_search`,
		`DROP TABLE memory_search_fragments`,
		`DROP TABLE memory_search`,
		`CREATE VIRTUAL TABLE memory_search USING fts5(filename, paths, body, note, tokenize='porter unicode61')`,
		`INSERT INTO memory_search(rowid, filename, paths, body, note) SELECT * FROM old_projection`,
		`CREATE VIRTUAL TABLE memory_search_fragments USING fts5(filename, paths, body, note, content='memory_search', tokenize='trigram')`,
		`INSERT INTO memory_search_fragments(rowid, filename, paths, body, note) SELECT rowid, filename, paths, body, note FROM memory_search`,
	} {
		if _, err := v.db.ExecContext(ctx, statement); err != nil {
			_ = v.Close()
			t.Fatal(err)
		}
	}
	originalPath := v.blobPath(memory.Blob.Ref)
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(originalPath); err != nil {
		t.Fatal(err)
	}
	v, err = openVault(ctx, root)
	if err != nil {
		t.Fatalf("upgrade attempted original Blob reread: %v", err)
	}
	defer func() { _ = v.Close() }()
	for _, query := range []string{"passports Anton", "sspo"} {
		page, err := v.SearchMemories(ctx, query, 10, "")
		if err != nil || page.Total != 1 || len(page.Items) != 1 ||
			page.Items[0].Memory.ID != memory.ID {
			t.Fatalf("upgraded search %q = %#v, %v", query, page, err)
		}
	}
}
