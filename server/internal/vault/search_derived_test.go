package vault

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSearchMemoriesFindsActiveDerivedDescription(t *testing.T) {
	ctx := context.Background()
	v := openInitializedSearchVault(t, ctx, t.TempDir())
	memory := putSearchFixture(t, ctx, v, "ordinary original text", ImportContext{
		OriginalFilename: "opaque-document.bin",
	})
	finishSearchDerivedRun(t, ctx, v, memory, DerivedContent{
		Content: "# Description\nA passport issued to Anton.\n",
		Blob:    BlobInfo{MediaType: "text/markdown"},
	})

	page, err := v.SearchMemories(ctx, "passport Anton", 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Memory.ID != memory.ID {
		t.Fatalf("active Derived Content search = total %d, items %#v", page.Total, page.Items)
	}
}

func TestSearchMemoriesIndexesMeaningfulStructuredValuesOnce(t *testing.T) {
	ctx := context.Background()
	v := openInitializedSearchVault(t, ctx, t.TempDir())
	memory := putSearchFixture(t, ctx, v, "originalonlyword source body", ImportContext{
		OriginalFilename: "opaque-source.dat",
	})
	finishSearchDerivedRun(t, ctx, v, memory,
		DerivedContent{
			Content: "# Description\nPassport issued to Anton.\n\x01private-marker\x02",
			Blob:    BlobInfo{MediaType: "text/markdown"},
		},
		DerivedContent{
			Content: `{"facts":[["document_number","ZX-481","Passport number ZX-481"]],` +
				`"events":[["issued",null,"to Anton","issue evidence"]],` +
				`"references":[["passport","REF-72",null,null,"reference evidence"]],` +
				`"signals":[["document_kind","passport","passport heading"],["game","ИГРЫ","game clue"],["short_tag","Ж","one character clue"]],` +
				`"uncertainties":["issue date unclear"],` +
				`"schema":"memoryd.document_data.v2",` +
				`"provenance":{"model":"codex-private-metadata"},` +
				`"warnings":["privatewarningtoken"]}`,
			Blob:       BlobInfo{MediaType: "application/json"},
			Provenance: []byte(`{"schema":"memoryd.document_data.v2"}`),
		},
		DerivedContent{
			Content: `{"total":42000,"currency":"AED","provenance":{"private":"outermetadata"}}`,
			Blob:    BlobInfo{MediaType: "application/json"},
		},
	)

	for _, query := range []string{"passport Anton", "originalonlyword passport", "descriptions", "sport", "иг", "ж", "ZX-481", "issue evidence", "REF-72", "unclear", "42000 AED"} {
		page, err := v.SearchMemories(ctx, query, 50, "")
		if err != nil {
			t.Fatalf("search %q: %v", query, err)
		}
		if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Memory.ID != memory.ID {
			t.Errorf("search %q = total %d, items %#v", query, page.Total, page.Items)
		}
	}

	page, err := v.SearchMemories(ctx, "passport Anton", 50, "")
	if err != nil {
		t.Fatal(err)
	}
	matched := false
	for _, part := range page.Items[0].Excerpt {
		if strings.ContainsAny(part.Text, "\x01\x02") {
			t.Fatalf("excerpt exposed highlight marker: %#v", part)
		}
		if part.Match && strings.Contains(strings.ToLower(part.Text), "passport") {
			matched = true
		}
	}
	if !matched {
		t.Fatalf("active Derived Content excerpt lacks marked match: %#v", page.Items[0].Excerpt)
	}
	for _, query := range []string{"codex-private-metadata", "privatewarningtoken", "outermetadata", "memoryd"} {
		page, err := v.SearchMemories(ctx, query, 50, "")
		if err != nil {
			t.Fatalf("metadata search %q: %v", query, err)
		}
		if page.Total != 0 {
			t.Errorf("processing metadata query %q matched %#v", query, page.Items)
		}
	}
}

func TestSearchMemoriesUsesOnlyActiveRunAndPreservesOnFailure(t *testing.T) {
	ctx := context.Background()
	v := openInitializedSearchVault(t, ctx, t.TempDir())
	memory := putSearchFixture(t, ctx, v, "ordinary original", ImportContext{
		OriginalFilename: "source.txt",
	})
	note := "private notephrase"
	if _, err := v.ResolveUnderstandingUserNote(ctx, memory.ID, &note); err != nil {
		t.Fatal(err)
	}
	firstRun := finishSearchDerivedRun(t, ctx, v, memory, DerivedContent{
		Content: "oldrunword passport description", Blob: BlobInfo{MediaType: "text/plain"},
	})
	assertDerivedSearchTotal(t, ctx, v, "oldrunword", 1)

	queuedAt := time.Now().UTC().Add(-time.Minute)
	startedAt := queuedAt.Add(time.Second)
	failedAttempt := UnderstandingAttempt{
		ID: uuid.New(), MemoryID: memory.ID, Status: UnderstandingStatusRunning,
		PluginID: "fixture", QueuedAt: queuedAt, StartedAt: &startedAt,
	}
	if _, err := v.FailUnderstanding(ctx, failedAttempt, UnderstandingDiagnostics{
		Error: "fixture failure",
	}); err != nil {
		t.Fatal(err)
	}
	assertDerivedSearchTotal(t, ctx, v, "oldrunword", 1)
	assertDerivedSearchTotal(t, ctx, v, "notephrase", 1)
	details, err := v.Understanding(ctx, memory.ID)
	if err != nil {
		t.Fatal(err)
	}
	if details.ActiveRun == nil || details.ActiveRun.ID != firstRun {
		t.Fatalf("failed attempt replaced active Run: %#v", details.ActiveRun)
	}

	secondRun := finishSearchDerivedRun(t, ctx, v, memory, DerivedContent{
		Content: "newrunword invoice summary", Blob: BlobInfo{MediaType: "text/markdown"},
	})
	if secondRun == firstRun {
		t.Fatal("successful replacement reused immutable Run identity")
	}
	assertDerivedSearchTotal(t, ctx, v, "oldrunword", 0)
	assertDerivedSearchTotal(t, ctx, v, "newrunword", 1)
	assertDerivedSearchTotal(t, ctx, v, "notephrase", 1)
	details, err = v.Understanding(ctx, memory.ID)
	if err != nil || details.ActiveRun == nil || details.ActiveRun.ID != secondRun ||
		details.UserNote != note {
		t.Fatalf("successful activation changed note or selected Run: %#v, %v", details, err)
	}
}

func TestSearchMemoriesClearsDerivedTextWhenActiveSelectionIsCleared(t *testing.T) {
	ctx := context.Background()
	v := openInitializedSearchVault(t, ctx, t.TempDir())
	memory := putSearchFixture(t, ctx, v, "basephrase remains", ImportContext{
		OriginalFilename: "source.txt",
	})
	finishSearchDerivedRun(t, ctx, v, memory, DerivedContent{
		Content: "activeonlyphrase", Blob: BlobInfo{MediaType: "text/plain"},
	})
	assertDerivedSearchTotal(t, ctx, v, "activeonlyphrase", 1)

	if _, err := v.db.ExecContext(
		ctx,
		`DELETE FROM active_understanding_runs WHERE memory_id = ?`,
		memory.ID.String(),
	); err != nil {
		t.Fatal(err)
	}
	assertDerivedSearchTotal(t, ctx, v, "activeonlyphrase", 0)
	assertDerivedSearchTotal(t, ctx, v, "basephrase", 1)
}

func TestSearchMemoriesProjectionFailureRollsBackActivation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	memory := putSearchFixture(t, ctx, v, "neutral original", ImportContext{
		OriginalFilename: "source.txt",
	})
	note := "saved note stays"
	if _, err := v.ResolveUnderstandingUserNote(ctx, memory.ID, &note); err != nil {
		t.Fatal(err)
	}
	firstRun := finishSearchDerivedRun(t, ctx, v, memory, DerivedContent{
		Content: "prioractivephrase passport", Blob: BlobInfo{MediaType: "text/plain"},
	})
	if _, err := v.db.ExecContext(ctx, `DROP TABLE memory_search_fragments`); err != nil {
		t.Fatal(err)
	}

	queuedAt := time.Now().UTC().Add(-time.Minute)
	startedAt := queuedAt.Add(time.Second)
	_, err = v.FinishUnderstanding(ctx, UnderstandingAttempt{
		ID: uuid.New(), MemoryID: memory.ID, Status: UnderstandingStatusRunning,
		PluginID: "fixture", QueuedAt: queuedAt, StartedAt: &startedAt,
	}, uuid.New(), "fixture-2", []DerivedContent{{
		Content: "uncommittednewphrase invoice",
		Blob:    BlobInfo{MediaType: "text/plain"},
	}}, nil, nil, nil, "")
	if err == nil {
		t.Fatal("activation succeeded after fragment projection was removed")
	}
	details, err := v.Understanding(ctx, memory.ID)
	if err != nil || details.ActiveRun == nil || details.ActiveRun.ID != firstRun ||
		details.UserNote != note {
		t.Fatalf("failed activation changed selected Run or note: %#v, %v", details, err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	assertDerivedSearchTotal(t, ctx, reopened, "prioractivephrase", 1)
	assertDerivedSearchTotal(t, ctx, reopened, "uncommittednewphrase", 0)
	assertDerivedSearchTotal(t, ctx, reopened, "saved note stays", 1)
}

func TestSearchMemoriesBackfillsActiveRunFromStoredArtifacts(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	memory := putSearchFixture(t, ctx, v, "neutral original body", ImportContext{
		OriginalFilename: "opaque.pdf",
	})
	finishSearchDerivedRun(t, ctx, v, memory, DerivedContent{
		Content: "backfillphrase passport description", Blob: BlobInfo{MediaType: "text/markdown"},
	})
	if _, err := v.db.ExecContext(ctx, deleteFragmentsSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := v.db.ExecContext(
		ctx,
		`UPDATE memory_search SET derived = '' WHERE rowid = (SELECT rowid FROM memories WHERE id = ?)`,
		memory.ID.String(),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := v.db.ExecContext(ctx, insertFragmentsSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := v.db.ExecContext(ctx, `DROP TABLE memory_search_derived`); err != nil {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	assertDerivedSearchTotal(t, ctx, reopened, "backfillphrase", 1)
}

func TestSearchMemoriesRejectsCorruptStoredSupportedArtifactOnBackfill(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	memory := putSearchFixture(t, ctx, v, "original remains intact", ImportContext{
		OriginalFilename: "opaque.pdf",
	})
	finishSearchDerivedRun(t, ctx, v, memory, DerivedContent{
		Content: "verified derived phrase", Blob: BlobInfo{MediaType: "text/plain"},
	})
	details, err := v.Understanding(ctx, memory.ID)
	if err != nil {
		t.Fatal(err)
	}
	artifactRef := details.ActiveRun.Artifacts[0].Blob.Ref
	if _, err := v.db.ExecContext(ctx, `DROP TABLE memory_search_derived`); err != nil {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(v.derivedBlobPath(artifactRef), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openVault(ctx, root); !errors.Is(err, ErrBlobUnavailable) {
		t.Fatalf("reopen error = %v, want ErrBlobUnavailable", err)
	}
	if _, err := os.Stat(v.blobPath(memory.Blob.Ref)); err != nil {
		t.Fatalf("corrupt Derived Content removed original Blob: %v", err)
	}
}

func TestSearchMemoriesKeepsBaseTextWhenDerivedContentIsEmptyOrUnsupported(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	empty := putSearchFixture(t, ctx, v, "basephrase for empty run", ImportContext{
		OriginalFilename: "empty.txt",
	})
	finishSearchDerivedRun(t, ctx, v, empty)
	emptyJSON := putSearchFixture(t, ctx, v, "basephrase for empty JSON run", ImportContext{
		OriginalFilename: "empty-json.txt",
	})
	finishSearchDerivedRun(t, ctx, v, emptyJSON, DerivedContent{
		Content: "", Blob: BlobInfo{MediaType: "application/json"},
	})
	unsupported := putSearchFixture(t, ctx, v, "basephrase for unsupported run", ImportContext{
		OriginalFilename: "unsupported.txt",
	})
	finishSearchDerivedRun(t, ctx, v, unsupported, DerivedContent{
		Content: "unsupportedphrase binary derived payload",
		Blob:    BlobInfo{MediaType: "application/octet-stream"},
	})
	assertDerivedSearchTotal(t, ctx, v, "basephrase", 3)
	assertDerivedSearchTotal(t, ctx, v, "unsupportedphrase", 0)
	details, err := v.Understanding(ctx, unsupported.ID)
	if err != nil || details.ActiveRun == nil || len(details.ActiveRun.Artifacts) != 1 {
		t.Fatalf("unsupported active Run = %#v, %v", details.ActiveRun, err)
	}
	if err := os.Remove(v.derivedBlobPath(details.ActiveRun.Artifacts[0].Blob.Ref)); err != nil {
		t.Fatal(err)
	}
	if _, err := v.db.ExecContext(
		ctx,
		`DELETE FROM memory_search_derived WHERE memory_id = ?`,
		unsupported.ID.String(),
	); err != nil {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	assertDerivedSearchTotal(t, ctx, reopened, "basephrase", 3)
	assertDerivedSearchTotal(t, ctx, reopened, "unsupportedphrase", 0)
}

func finishSearchDerivedRun(
	t *testing.T,
	ctx context.Context,
	v *Vault,
	memory Memory,
	artifacts ...DerivedContent,
) uuid.UUID {
	t.Helper()
	queuedAt := time.Now().UTC().Add(-time.Minute)
	startedAt := queuedAt.Add(time.Second)
	runID := uuid.New()
	if _, err := v.FinishUnderstanding(ctx, UnderstandingAttempt{
		ID: uuid.New(), MemoryID: memory.ID, Status: UnderstandingStatusRunning,
		PluginID: "fixture", QueuedAt: queuedAt, StartedAt: &startedAt,
	}, runID, "fixture-1", artifacts, nil, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	return runID
}

func assertDerivedSearchTotal(
	t *testing.T,
	ctx context.Context,
	v *Vault,
	query string,
	want int64,
) {
	t.Helper()
	page, err := v.SearchMemories(ctx, query, 50, "")
	if err != nil {
		t.Fatalf("search %q: %v", query, err)
	}
	if page.Total != want {
		t.Fatalf("search %q total = %d, want %d: %#v", query, page.Total, want, page.Items)
	}
}

func TestSearchMemoriesRestoresDerivedContentWhenProjectionRowIsMissing(t *testing.T) {
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
		"originalneedle body",
		ImportContext{OriginalFilename: "opaque.txt"},
	)
	finishSearchDerivedRun(t, ctx, v, memory, DerivedContent{
		Content: "derivedneedle passport", Blob: BlobInfo{MediaType: "text/markdown"},
	})
	if _, err := v.db.ExecContext(ctx, deleteFragmentsSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := v.db.ExecContext(ctx, `DELETE FROM memory_search`); err != nil {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	v, err = openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = v.Close() }()
	assertDerivedSearchTotal(t, ctx, v, "originalneedle", 1)
	assertDerivedSearchTotal(t, ctx, v, "derivedneedle", 1)
	assertDerivedSearchTotal(t, ctx, v, "ivedneedle", 1)
}

func TestSearchMemoriesKeepsLiteralUnparseableJSONArtifactsValid(t *testing.T) {
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
		"originalneedle retained",
		ImportContext{OriginalFilename: "opaque.txt"},
	)
	for _, content := range []string{"unsupportedneedle literal", `{"value":"unsupportedneedle"} trailing`, `{"value":"unsupportedneedle"} {}`} {
		finishSearchDerivedRun(
			t,
			ctx,
			v,
			memory,
			DerivedContent{Content: content, Blob: BlobInfo{MediaType: "application/json"}},
			DerivedContent{
				Content: "companionneedle supported description",
				Blob:    BlobInfo{MediaType: "text/plain"},
			},
		)
		assertDerivedSearchTotal(t, ctx, v, "originalneedle companionneedle", 1)
		assertDerivedSearchTotal(t, ctx, v, "unsupportedneedle", 0)
	}
	if _, err := v.db.ExecContext(ctx, `DROP TABLE memory_search_derived`); err != nil {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	v, err = openVault(ctx, root)
	if err != nil {
		t.Fatalf("literal JSON artifact prevented compatible Vault backfill: %v", err)
	}
	defer func() { _ = v.Close() }()
	details, err := v.Understanding(ctx, memory.ID)
	if err != nil || details.ActiveRun == nil ||
		details.ActiveRun.Artifacts[0].Content != `{"value":"unsupportedneedle"} {}` {
		t.Fatalf("literal artifact was not retained: %#v, %v", details.ActiveRun, err)
	}
	assertDerivedSearchTotal(t, ctx, v, "originalneedle companionneedle", 1)
	assertDerivedSearchTotal(t, ctx, v, "unsupportedneedle", 0)
}
