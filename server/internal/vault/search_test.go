package vault

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestInitializeSearchBackfillsMissingRowsAndNormalizesMarkers(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = v.Close() }()

	content := "body\x01marker and \x02marker"
	memory := putSearchFixture(t, ctx, v, content, ImportContext{
		OriginalFilename: "name\x01.txt",
		RelativePath:     "relative\x02path/name.txt",
		FullPath:         "/full\x01path/name.txt",
	})
	note := "note\x02marker"
	if _, err := v.ResolveUnderstandingUserNote(ctx, memory.ID, &note); err != nil {
		t.Fatal(err)
	}
	if err := dropSearchProjection(v.db); err != nil {
		t.Fatal(err)
	}
	if err := v.initializeSearch(ctx); err != nil {
		t.Fatalf("initialize search backfill: %v", err)
	}

	var rowid int64
	var filename, paths, body, indexedNote string
	if err := v.db.QueryRowContext(ctx, `
		SELECT memory_search.rowid, filename, paths, body, note
		FROM memory_search
		JOIN memories ON memories.rowid = memory_search.rowid
		WHERE memories.id = ?
	`, memory.ID.String()).Scan(&rowid, &filename, &paths, &body, &indexedNote); err != nil {
		t.Fatal(err)
	}
	var memoryRowID int64
	if err := v.db.QueryRowContext(ctx,
		`SELECT rowid FROM memories WHERE id = ?`, memory.ID.String(),
	).Scan(&memoryRowID); err != nil {
		t.Fatal(err)
	}
	if rowid != memoryRowID {
		t.Fatalf("search rowid = %d, Memory rowid = %d", rowid, memoryRowID)
	}
	if filename != memory.ImportContext.OriginalFilename ||
		paths != "relative path/name.txt\n/full path/name.txt" ||
		body != "body marker and  marker" ||
		indexedNote != "note marker" {
		t.Fatalf("search projection = filename %q, paths %q, body %q, note %q",
			filename, paths, body, indexedNote)
	}
	if countSearchRows(t, ctx, v, memory.ID) != 1 {
		t.Fatal("backfill did not create exactly one search row")
	}
}

func TestSearchProjectionTracksNotesAndDeletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	v, err := openVault(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = v.Close() }()
	if err := v.initializeSearch(ctx); err != nil {
		t.Fatal(err)
	}

	memory := putSearchFixture(t, ctx, v, "bodyword stays", ImportContext{
		OriginalFilename: "opaque.txt",
	})
	initial := searchProjection(t, ctx, v, memory.ID)
	if initial.body != "bodyword stays" || initial.note != "" {
		t.Fatalf("initial projection = %#v", initial)
	}

	_, err = v.Put(ctx, Import{
		Content:       strings.NewReader("bodyword stays"),
		MediaTypeHint: "text/plain",
		Context:       ImportContext{OriginalFilename: "renamed.txt"},
	})
	var duplicate *DuplicateError
	if !errors.As(err, &duplicate) || duplicate.Existing.ID != memory.ID {
		t.Fatalf("duplicate import error = %v, want existing Memory %s", err, memory.ID)
	}
	if got := searchProjection(t, ctx, v, memory.ID); got.filename != "opaque.txt" ||
		got.body != "bodyword stays" {
		t.Fatalf("duplicate import changed search projection: %#v", got)
	}

	firstNote := "first\x01note"
	if _, err := v.ResolveUnderstandingUserNote(ctx, memory.ID, &firstNote); err != nil {
		t.Fatal(err)
	}
	if got := searchProjection(t, ctx, v, memory.ID).note; got != "first note" {
		t.Fatalf("inserted note projection = %q", got)
	}
	secondNote := "replacement\x02note"
	if _, err := v.ResolveUnderstandingUserNote(ctx, memory.ID, &secondNote); err != nil {
		t.Fatal(err)
	}
	if got := searchProjection(t, ctx, v, memory.ID).note; got != "replacement note" {
		t.Fatalf("updated note projection = %q", got)
	}
	cleared := ""
	if _, err := v.ResolveUnderstandingUserNote(ctx, memory.ID, &cleared); err != nil {
		t.Fatal(err)
	}
	if got := searchProjection(t, ctx, v, memory.ID).note; got != "" {
		t.Fatalf("cleared note projection = %q", got)
	}
	if got := countSearchMatches(t, ctx, v, "replacement"); got != 0 {
		t.Fatalf("cleared note still matches: %d", got)
	}

	deleteNote := "deletetriggerword"
	if _, err := v.ResolveUnderstandingUserNote(ctx, memory.ID, &deleteNote); err != nil {
		t.Fatal(err)
	}
	if _, err := v.db.ExecContext(ctx,
		`DELETE FROM memory_understanding_notes WHERE memory_id = ?`, memory.ID.String(),
	); err != nil {
		t.Fatal(err)
	}
	if got := searchProjection(t, ctx, v, memory.ID).note; got != "" {
		t.Fatalf("deleted note remains indexed: %q", got)
	}
	if got := countSearchMatches(t, ctx, v, deleteNote); got != 0 {
		t.Fatalf("deleted note still matches: %d", got)
	}

	if err := v.Delete(ctx, memory.ID); err != nil {
		t.Fatal(err)
	}
	if countSearchRows(t, ctx, v, memory.ID) != 0 {
		t.Fatal("deleted Memory remains in search index")
	}
}

func TestSearchOriginalBodySupportsOnlyVerifiedText(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	v, err := openVault(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = v.Close() }()

	for _, test := range []struct {
		name      string
		mediaType string
		content   []byte
		want      string
	}{
		{name: "valid UTF-8 text", mediaType: "text/plain", content: []byte("café"), want: "café"},
		{name: "non-text", mediaType: "application/octet-stream", content: []byte{0, 0xff}, want: ""},
		{name: "invalid UTF-8 text", mediaType: "text/plain", content: []byte{0xff}, want: ""},
		{name: "NUL text", mediaType: "text/markdown", content: []byte("a\x00b"), want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			ref := NewSHA256Blobref(sha256.Sum256(test.content))
			path := v.blobPath(ref)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, test.content, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := v.searchOriginalBody(Memory{Blob: BlobInfo{
				Ref: ref, MediaType: test.mediaType, ByteSize: int64(len(test.content)),
			}})
			if err != nil || got != test.want {
				t.Fatalf("searchOriginalBody() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestSearchBackfillRejectsMissingOrCorruptTextBlob(t *testing.T) {
	for _, broken := range []string{"missing", "corrupt"} {
		t.Run(broken, func(t *testing.T) {
			ctx := context.Background()
			v, err := openVault(ctx, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = v.Close() }()
			if err := v.initializeSearch(ctx); err != nil {
				t.Fatal(err)
			}
			memory := putSearchFixture(t, ctx, v, "verified source", ImportContext{
				OriginalFilename: "source.txt",
			})
			if err := dropSearchProjection(v.db); err != nil {
				t.Fatal(err)
			}
			path := v.blobPath(memory.Blob.Ref)
			if broken == "missing" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte("corrupt content"), 0o600); err != nil {
				t.Fatal(err)
			}

			err = v.initializeSearch(ctx)
			if !errors.Is(err, ErrBlobUnavailable) {
				t.Fatalf("initializeSearch() error = %v, want ErrBlobUnavailable", err)
			}
			var count int
			if err := v.db.QueryRowContext(ctx,
				`SELECT count(*) FROM memories WHERE id = ?`, memory.ID.String(),
			).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("failed backfill changed authoritative Memory count to %d", count)
			}
			if strings.Contains(err.Error(), memory.ID.String()) == false {
				t.Fatalf("backfill error lacks Memory context: %v", err)
			}
		})
	}
}

func TestSearchProjectionMutationFailuresRollbackAndRecover(t *testing.T) {
	t.Run("import", func(t *testing.T) {
		ctx := context.Background()
		root := t.TempDir()
		v := openInitializedSearchVault(t, ctx, root)
		original := putSearchFixture(t, ctx, v, "preserved bodyword", ImportContext{
			OriginalFilename: "preserved.txt",
		})
		if err := dropSearchTable(v.db); err != nil {
			t.Fatal(err)
		}

		candidate := []byte("failed insertion candidateword")
		_, err := v.Put(ctx, Import{
			Content:       bytes.NewReader(candidate),
			MediaTypeHint: "text/plain",
			Context:       ImportContext{OriginalFilename: "candidate.txt"},
		})
		if err == nil {
			t.Fatal("Put succeeded without derived search table")
		}
		candidateRef := NewSHA256Blobref(sha256.Sum256(candidate))
		if err := verifyBlob(
			v.blobPath(candidateRef),
			candidateRef,
			int64(len(candidate)),
		); err != nil {
			t.Fatalf("published Blob did not survive failed import: %v", err)
		}
		assertMemoryCount(t, ctx, v, candidateRef, 0)
		v = reopenSearchVault(t, ctx, root, v)
		if countSearchMatches(t, ctx, v, "bodyword") != 1 ||
			countSearchMatches(t, ctx, v, "candidateword") != 0 {
			t.Fatal("reopened search index does not reflect committed Memories only")
		}
		if countSearchRows(t, ctx, v, original.ID) != 1 {
			t.Fatal("reopen lost existing search projection")
		}
	})

	t.Run("note", func(t *testing.T) {
		ctx := context.Background()
		root := t.TempDir()
		v := openInitializedSearchVault(t, ctx, root)
		memory := putSearchFixture(t, ctx, v, "stable body", ImportContext{
			OriginalFilename: "note-target.txt",
		})
		before := "beforeword"
		if _, err := v.ResolveUnderstandingUserNote(ctx, memory.ID, &before); err != nil {
			t.Fatal(err)
		}
		if err := dropSearchTable(v.db); err != nil {
			t.Fatal(err)
		}
		after := "afterword"
		if _, err := v.ResolveUnderstandingUserNote(ctx, memory.ID, &after); err == nil {
			t.Fatal("note update succeeded without derived search table")
		}
		if got, err := v.ResolveUnderstandingUserNote(
			ctx,
			memory.ID,
			nil,
		); err != nil ||
			got != before {
			t.Fatalf("note after failed update = %q, %v; want %q", got, err, before)
		}
		if _, err := os.Stat(v.blobPath(memory.Blob.Ref)); err != nil {
			t.Fatalf("original Blob unavailable after failed note update: %v", err)
		}
		v = reopenSearchVault(t, ctx, root, v)
		if got := searchProjection(t, ctx, v, memory.ID).note; got != before {
			t.Fatalf("reopened note projection = %q, want %q", got, before)
		}
	})

	t.Run("delete", func(t *testing.T) {
		ctx := context.Background()
		root := t.TempDir()
		v := openInitializedSearchVault(t, ctx, root)
		memory := putSearchFixture(t, ctx, v, "deleteword body", ImportContext{
			OriginalFilename: "delete-target.txt",
		})
		if err := dropSearchTable(v.db); err != nil {
			t.Fatal(err)
		}
		if err := v.Delete(ctx, memory.ID); err == nil {
			t.Fatal("Delete succeeded without derived search table")
		}
		if _, err := v.Memory(ctx, memory.ID); err != nil {
			t.Fatalf("Memory removed after failed delete: %v", err)
		}
		if _, err := os.Stat(v.blobPath(memory.Blob.Ref)); err != nil {
			t.Fatalf("original Blob unavailable after failed delete: %v", err)
		}
		v = reopenSearchVault(t, ctx, root, v)
		if countSearchMatches(t, ctx, v, "deleteword") != 1 ||
			countSearchRows(t, ctx, v, memory.ID) != 1 {
			t.Fatal("reopen did not restore search projection for rolled-back delete")
		}
	})
}

func putSearchFixture(
	t *testing.T,
	ctx context.Context,
	v *Vault,
	content string,
	importContext ImportContext,
) Memory {
	t.Helper()
	memory, err := v.Put(ctx, Import{
		Content:       strings.NewReader(content),
		MediaTypeHint: "text/plain",
		Context:       importContext,
	})
	if err != nil {
		t.Fatal(err)
	}
	return memory
}

func openInitializedSearchVault(t *testing.T, ctx context.Context, root string) *Vault {
	t.Helper()
	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.initializeSearch(ctx); err != nil {
		_ = v.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	return v
}

func reopenSearchVault(t *testing.T, ctx context.Context, root string, old *Vault) *Vault {
	t.Helper()
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.initializeSearch(ctx); err != nil {
		_ = v.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	return v
}

func dropSearchProjection(database *sql.DB) error {
	for _, name := range []string{
		"memory_search_memory_delete",
		"memory_search_note_insert",
		"memory_search_note_update",
		"memory_search_note_delete",
	} {
		if _, err := database.Exec(`DROP TRIGGER IF EXISTS ` + name); err != nil {
			return err
		}
	}
	return dropSearchTable(database)
}

func dropSearchTable(database *sql.DB) error {
	_, err := database.Exec(`DROP TABLE memory_search`)
	return err
}

type searchProjectionRow struct {
	filename string
	paths    string
	body     string
	note     string
}

func searchProjection(
	t *testing.T,
	ctx context.Context,
	v *Vault,
	id interface{ String() string },
) searchProjectionRow {
	t.Helper()
	var row searchProjectionRow
	if err := v.db.QueryRowContext(ctx, `
		SELECT filename, paths, body, note FROM memory_search
		WHERE rowid = (SELECT rowid FROM memories WHERE id = ?)
	`, id.String()).Scan(&row.filename, &row.paths, &row.body, &row.note); err != nil {
		t.Fatal(err)
	}
	return row
}

func countSearchRows(
	t *testing.T,
	ctx context.Context,
	v *Vault,
	id interface{ String() string },
) int {
	t.Helper()
	var count int
	if err := v.db.QueryRowContext(ctx, `
		SELECT count(*) FROM memory_search
		WHERE rowid = (SELECT rowid FROM memories WHERE id = ?)
	`, id.String()).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func countSearchMatches(t *testing.T, ctx context.Context, v *Vault, term string) int {
	t.Helper()
	var count int
	if err := v.db.QueryRowContext(ctx,
		`SELECT count(*) FROM memory_search WHERE memory_search MATCH ?`, term,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertMemoryCount(t *testing.T, ctx context.Context, v *Vault, ref Blobref, want int) {
	t.Helper()
	var count int
	if err := v.db.QueryRowContext(ctx,
		`SELECT count(*) FROM memories WHERE blob_hash = ?`, ref.String(),
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("Memory count for %s = %d, want %d", ref, count, want)
	}
}

func TestSearchMemoriesMandatoryTermsAndExcerpts(t *testing.T) {
	ctx := context.Background()
	v := openInitializedSearchVault(t, ctx, t.TempDir())
	target := putSearchFixture(t, ctx, v, "Anton passport", ImportContext{
		OriginalFilename: "opaque-target.txt",
	})
	putSearchFixture(t, ctx, v, "Blair passport", ImportContext{
		OriginalFilename: "other-target.txt",
	})
	pathOnly := putSearchFixture(t, ctx, v, "neutral route content", ImportContext{
		OriginalFilename: "passport-path-only.txt",
		RelativePath:     "archive/passport",
	})
	putSearchFixture(t, ctx, v, "Vesuvius only", ImportContext{
		OriginalFilename: "volcano.txt",
	})

	page, err := v.SearchMemories(ctx, "  passports, Anton!  ", 50)
	if err != nil {
		t.Fatal(err)
	}
	if page.QueryPlan.Query != "passports, Anton!" ||
		strings.Join(page.QueryPlan.Terms, ",") != "passports,anton" {
		t.Fatalf("query plan = %#v", page.QueryPlan)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Memory.ID != target.ID {
		t.Fatalf("mandatory search result = total %d, items %#v", page.Total, page.Items)
	}
	markedPassport := false
	for _, part := range page.Items[0].Excerpt {
		if strings.ContainsAny(part.Text, "\x01\x02") {
			t.Fatalf("excerpt exposed marker bytes: %#v", part)
		}
		if part.Match && strings.EqualFold(part.Text, "passport") {
			markedPassport = true
		}
	}
	if !markedPassport {
		t.Fatalf("passport term missing from marked excerpt: %#v", page.Items[0].Excerpt)
	}

	pathPage, err := v.SearchMemories(ctx, "passport", 50)
	if err != nil {
		t.Fatal(err)
	}
	foundPathOnly := false
	for _, hit := range pathPage.Items {
		if hit.Memory.ID == pathOnly.ID {
			foundPathOnly = true
			if hit.Excerpt == nil || len(hit.Excerpt) != 0 {
				t.Fatalf("filename/path-only hit has excerpt: %#v", hit.Excerpt)
			}
		}
	}
	if !foundPathOnly {
		t.Fatal("filename/path-only Memory did not match")
	}

	operatorPage, err := v.SearchMemories(ctx, "Anton OR Vesuvius", 50)
	if err != nil {
		t.Fatal(err)
	}
	if operatorPage.Total != 0 || operatorPage.Items == nil || len(operatorPage.Items) != 0 ||
		strings.Join(operatorPage.QueryPlan.Terms, ",") != "anton,or,vesuvius" {
		t.Fatalf("literal OR query = %#v", operatorPage)
	}
	punctuationPage, err := v.SearchMemories(ctx, "!!passports... Anton??", 50)
	if err != nil {
		t.Fatal(err)
	}
	if punctuationPage.Total != 1 || punctuationPage.Items[0].Memory.ID != target.ID {
		t.Fatalf("punctuation-wrapped query = %#v", punctuationPage)
	}
}

func TestSearchMemoriesUnicodeNumbersAndValidation(t *testing.T) {
	ctx := context.Background()
	v := openInitializedSearchVault(t, ctx, t.TempDir())
	russian := putSearchFixture(t, ctx, v, "Сегодня игры начинаются", ImportContext{
		OriginalFilename: "russian.txt",
	})
	numeric := putSearchFixture(t, ctx, v, "report 2026 archive", ImportContext{
		OriginalFilename: "annual-report.txt",
	})

	russianPage, err := v.SearchMemories(ctx, "игры", 1)
	if err != nil || russianPage.Total != 1 || len(russianPage.Items) != 1 ||
		russianPage.Items[0].Memory.ID != russian.ID {
		t.Fatalf("Russian search = %#v, %v", russianPage, err)
	}
	numericPage, err := v.SearchMemories(ctx, "2026", 1)
	if err != nil || numericPage.Total != 1 || len(numericPage.Items) != 1 ||
		numericPage.Items[0].Memory.ID != numeric.ID ||
		strings.Join(numericPage.QueryPlan.Terms, ",") != "2026" {
		t.Fatalf("numeric search = %#v, %v", numericPage, err)
	}

	plan := compileSearchPlan("  Cafe\u0301—ИГРЫ 2026 +\u0301  ")
	if plan.Query != "Cafe\u0301—ИГРЫ 2026 +\u0301" ||
		strings.Join(plan.Terms, ",") != "cafe\u0301,игры,2026" {
		t.Fatalf("Unicode query compilation = %#v", plan)
	}
	for _, query := range []string{"", " \t ", "?!—"} {
		if _, err := v.SearchMemories(ctx, query, 1); !errors.Is(err, ErrInvalidSearchQuery) {
			t.Errorf("SearchMemories(%q) error = %v, want ErrInvalidSearchQuery", query, err)
		}
	}
	for _, limit := range []int64{0, -1} {
		if _, err := v.SearchMemories(ctx, "games", limit); !errors.Is(err, ErrInvalidSearchLimit) {
			t.Errorf("SearchMemories limit %d error = %v, want ErrInvalidSearchLimit", limit, err)
		}
	}
}

func TestSearchMemoriesCountLimitAndOrdering(t *testing.T) {
	ctx := context.Background()
	v := openInitializedSearchVault(t, ctx, t.TempDir())
	for i := range 105 {
		suffix := strconv.Itoa(i)
		putSearchFixture(t, ctx, v, "bulkterm item "+suffix, ImportContext{
			OriginalFilename: "bulk-" + suffix + ".txt",
		})
	}
	limited, err := v.SearchMemories(ctx, "bulkterm", 2)
	if err != nil {
		t.Fatal(err)
	}
	if limited.Total != 105 || len(limited.Items) != 2 {
		t.Fatalf("limited page = total %d, items %d", limited.Total, len(limited.Items))
	}
	large, err := v.SearchMemories(ctx, "bulkterm", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if large.Total != 105 || len(large.Items) != 105 {
		t.Fatalf("large-limit page = total %d, items %d", large.Total, len(large.Items))
	}

	tieVault := openInitializedSearchVault(t, ctx, t.TempDir())
	first := putSearchFixture(t, ctx, tieVault, "tieword alpha", ImportContext{
		OriginalFilename: "first-tie.txt",
	})
	second := putSearchFixture(t, ctx, tieVault, "alpha tieword", ImportContext{
		OriginalFilename: "second-tie.txt",
	})
	ties, err := tieVault.SearchMemories(ctx, "tieword", 10)
	if err != nil {
		t.Fatal(err)
	}
	if ties.Total != 2 || len(ties.Items) != 2 {
		t.Fatalf("equal-score result count = total %d, items %d", ties.Total, len(ties.Items))
	}
	wantIDs := []string{first.ID.String(), second.ID.String()}
	sort.Strings(wantIDs)
	gotIDs := []string{ties.Items[0].Memory.ID.String(), ties.Items[1].Memory.ID.String()}
	if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("tie ordering = %v, want ID order %v", gotIDs, wantIDs)
	}

	rankVault := openInitializedSearchVault(t, ctx, t.TempDir())
	short := putSearchFixture(t, ctx, rankVault, "rankword", ImportContext{
		OriginalFilename: "short.txt",
	})
	long := putSearchFixture(
		t,
		ctx,
		rankVault,
		"rankword "+strings.Repeat("background ", 80),
		ImportContext{
			OriginalFilename: "long.txt",
		},
	)
	ranked, err := rankVault.SearchMemories(ctx, "rankword", 10)
	if err != nil {
		t.Fatal(err)
	}
	if ranked.Total != 2 || len(ranked.Items) != 2 ||
		ranked.Items[0].Memory.ID != short.ID || ranked.Items[1].Memory.ID != long.ID {
		t.Fatalf("BM25 order = %#v", ranked.Items)
	}
}

func TestSearchMemoriesTracksNotesDeletionAndBackfill(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	v := openInitializedSearchVault(t, ctx, root)
	memory := putSearchFixture(
		t,
		ctx,
		v,
		"bodypriority content stable searchable body",
		ImportContext{
			OriginalFilename: "opaque.txt",
		},
	)
	oldNote, newNote := "oldnoteunique", "newnoteunique bodypriority notecontext"
	if _, err := v.ResolveUnderstandingUserNote(ctx, memory.ID, &oldNote); err != nil {
		t.Fatal(err)
	}
	oldPage, err := v.SearchMemories(ctx, oldNote, 10)
	if err != nil || oldPage.Total != 1 || len(oldPage.Items) != 1 ||
		oldPage.Items[0].Memory.ID != memory.ID {
		t.Fatalf("saved-note result = %#v, %v", oldPage, err)
	}
	if _, err := v.ResolveUnderstandingUserNote(ctx, memory.ID, &newNote); err != nil {
		t.Fatal(err)
	}
	if oldPage, err = v.SearchMemories(ctx, oldNote, 10); err != nil || oldPage.Total != 0 {
		t.Fatalf("replaced note still searchable: %#v, %v", oldPage, err)
	}
	newPage, err := v.SearchMemories(ctx, newNote, 10)
	if err != nil || newPage.Total != 1 || len(newPage.Items) != 1 ||
		newPage.Items[0].Memory.ID != memory.ID {
		t.Fatalf("replacement note result = %#v, %v", newPage, err)
	}

	clearedNote := ""
	if _, err := v.ResolveUnderstandingUserNote(ctx, memory.ID, &clearedNote); err != nil {
		t.Fatal(err)
	}
	clearedPage, err := v.SearchMemories(ctx, newNote, 10)
	if err != nil || clearedPage.Total != 0 || len(clearedPage.Items) != 0 {
		t.Fatalf("cleared note remains searchable: %#v, %v", clearedPage, err)
	}
	if _, err := v.ResolveUnderstandingUserNote(ctx, memory.ID, &newNote); err != nil {
		t.Fatal(err)
	}

	if err := dropSearchProjection(v.db); err != nil {
		t.Fatal(err)
	}
	v = reopenSearchVault(t, ctx, root, v)
	backfilled, err := v.SearchMemories(ctx, newNote, 10)
	if err != nil || backfilled.Total != 1 || len(backfilled.Items) != 1 ||
		backfilled.Items[0].Memory.ID != memory.ID {
		t.Fatalf("reopened backfilled note result = %#v, %v", backfilled, err)
	}
	if err := v.Delete(ctx, memory.ID); err != nil {
		t.Fatal(err)
	}
	deleted, err := v.SearchMemories(ctx, newNote, 10)
	if err != nil || deleted.Total != 0 || deleted.Items == nil || len(deleted.Items) != 0 {
		t.Fatalf("deleted Memory search result = %#v, %v", deleted, err)
	}
}

func TestSearchMemoriesChoosesMatchingExcerpt(t *testing.T) {
	ctx := context.Background()
	v := openInitializedSearchVault(t, ctx, t.TempDir())
	memory := putSearchFixture(t, ctx, v, "bodypriority searchable body", ImportContext{
		OriginalFilename: "opaque.txt",
	})
	note := "noteunique bodypriority notecontext"
	if _, err := v.ResolveUnderstandingUserNote(ctx, memory.ID, &note); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"noteunique", "bodypriority"} {
		page, err := v.SearchMemories(ctx, query, 10)
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("query %q: %#v, %v", query, page, err)
		}
		var excerpt strings.Builder
		matched := false
		for _, part := range page.Items[0].Excerpt {
			excerpt.WriteString(part.Text)
			matched = matched || part.Match && part.Text == query
		}
		if !matched {
			t.Fatalf("query %q has no matching excerpt: %#v", query, page.Items[0].Excerpt)
		}
		if query == "bodypriority" && strings.Contains(excerpt.String(), "notecontext") {
			t.Fatalf("matching note replaced matching body: %q", excerpt.String())
		}
	}
}
