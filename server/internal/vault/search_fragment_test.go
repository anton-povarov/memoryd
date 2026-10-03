package vault

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"
)

func TestSearchMemoriesSubstringFragmentsAcrossFields(t *testing.T) {
	ctx := context.Background()
	v := openInitializedSearchVault(t, ctx, t.TempDir())
	target := putSearchFixture(t, ctx, v, "oldCIV2backup microAntonrecord", ImportContext{
		OriginalFilename: "passports.txt",
	})
	bodyOnly := putSearchFixture(t, ctx, v, "another microAntonrecord", ImportContext{
		OriginalFilename: "body-only.txt",
	})
	filenameOnly := putSearchFixture(t, ctx, v, "neutral content", ImportContext{
		OriginalFilename: "oldCIV2backup.txt",
		RelativePath:     "archive/pathCIV2backup",
	})

	page, err := v.SearchMemories(ctx, "anton passports", 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Memory.ID != target.ID {
		t.Fatalf("mixed-field mandatory query = %#v", page)
	}

	for query, want := range map[string]string{"iv2": "IV2", "anton": "Anton"} {
		page, err := v.SearchMemories(ctx, query, 20, "")
		if err != nil {
			t.Fatal(err)
		}
		if page.Total == 0 {
			t.Fatalf("substring query %q found no Memories", query)
		}
		foundTarget, foundBodyOnly := false, false
		for _, hit := range page.Items {
			if hit.Memory.ID == target.ID {
				foundTarget = true
				if matchedExcerptText(hit.Excerpt) != want {
					t.Errorf(
						"query %q matched excerpt = %q, want original-case %q",
						query,
						matchedExcerptText(hit.Excerpt),
						want,
					)
				}
			}
			foundBodyOnly = foundBodyOnly || hit.Memory.ID == bodyOnly.ID
			if hit.Memory.ID == filenameOnly.ID && (hit.Excerpt == nil || len(hit.Excerpt) != 0) {
				t.Errorf("metadata-only hit %q has excerpt %#v", query, hit.Excerpt)
			}
		}
		if !foundTarget {
			t.Errorf("substring query %q omitted target Memory", query)
		}
		if query == "anton" && !foundBodyOnly {
			t.Error("body fragment omitted second matching Memory")
		}
	}

	overlap, err := v.SearchMemories(ctx, "ant anton ant", 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if overlap.Total != 2 || len(overlap.Items) != 2 {
		t.Fatalf("overlapping/repeated fragment terms = %#v", overlap)
	}
	overlapIDs := make(map[string]bool)
	for _, hit := range overlap.Items {
		id := hit.Memory.ID.String()
		if overlapIDs[id] || (hit.Memory.ID != target.ID && hit.Memory.ID != bodyOnly.ID) {
			t.Fatalf(
				"overlapping query returned duplicate or unexpected Memory: %#v",
				overlap.Items,
			)
		}
		overlapIDs[id] = true
	}

	upperRussian := putSearchFixture(t, ctx, v, "ПРИВЕТ СЛОЖНОЕ", ImportContext{
		OriginalFilename: "russian-uppercase.txt",
	})
	shortRussian := putSearchFixture(t, ctx, v, "СТРАНА", ImportContext{
		OriginalFilename: "russian-short.txt",
	})
	for _, test := range []struct {
		query string
		id    string
	}{
		{query: "ивет", id: upperRussian.ID.String()},
		{query: "ж", id: upperRussian.ID.String()},
		{query: "ан", id: shortRussian.ID.String()},
	} {
		page, err := v.SearchMemories(ctx, test.query, 20, "")
		if err != nil {
			t.Errorf("SearchMemories(%q): %v", test.query, err)
			continue
		}
		found := false
		for _, hit := range page.Items {
			found = found || hit.Memory.ID.String() == test.id
		}
		if !found {
			t.Errorf(
				"case-insensitive short fragment %q omitted Memory %s: %#v",
				test.query,
				test.id,
				page.Items,
			)
		}
	}
}

func TestSearchMemoriesFragmentTierAndStableTies(t *testing.T) {
	ctx := context.Background()
	v := openInitializedSearchVault(t, ctx, t.TempDir())
	precise := putSearchFixture(
		t,
		ctx,
		v,
		"needle "+strings.Repeat("background ", 120),
		ImportContext{
			OriginalFilename: "precise.txt",
		},
	)
	putSearchFixture(t, ctx, v, "microneedleX", ImportContext{
		OriginalFilename: "fragment.txt",
	})

	limited, err := v.SearchMemories(ctx, "needle", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if limited.Total != 2 || len(limited.Items) != 1 || limited.Items[0].Memory.ID != precise.ID {
		t.Fatalf("precise-before-fragment limited page = %#v", limited)
	}

	first := putSearchFixture(t, ctx, v, "aaaCIV2bbb", ImportContext{OriginalFilename: "tie-a.txt"})
	second := putSearchFixture(
		t,
		ctx,
		v,
		"cccCIV2ddd",
		ImportContext{OriginalFilename: "tie-b.txt"},
	)
	wantIDs := []string{first.ID.String(), second.ID.String()}
	sort.Strings(wantIDs)
	for attempt := range 3 {
		page, err := v.SearchMemories(ctx, "iv2 civ2 iv2", 20, "")
		if err != nil {
			t.Fatal(err)
		}
		gotIDs := make([]string, 0, len(page.Items))
		seen := make(map[string]bool)
		for _, hit := range page.Items {
			id := hit.Memory.ID.String()
			if seen[id] {
				t.Fatalf("Memory %s appeared more than once: %#v", id, page.Items)
			}
			seen[id] = true
			if id == first.ID.String() || id == second.ID.String() {
				gotIDs = append(gotIDs, id)
			}
		}
		if len(gotIDs) != len(wantIDs) || strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
			t.Fatalf("same-tier tie order attempt %d = %v, want %v", attempt, gotIDs, wantIDs)
		}
	}
}

func TestSearchMemoriesFragmentNotesBackfillAndReopen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	v := openInitializedSearchVault(t, ctx, root)
	memory := putSearchFixture(t, ctx, v, "original microAntonrecord", ImportContext{
		OriginalFilename: "opaque.txt",
	})
	oldNote, newNote := "microCIV2oldnote", "updatedmicroIV2newnote"
	if _, err := v.ResolveUnderstandingUserNote(ctx, memory.ID, &oldNote); err != nil {
		t.Fatal(err)
	}
	if page, err := v.SearchMemories(ctx, "iv2", 20, ""); err != nil || page.Total != 1 {
		t.Fatalf("initial note fragment = %#v, %v", page, err)
	}
	if _, err := v.ResolveUnderstandingUserNote(ctx, memory.ID, &newNote); err != nil {
		t.Fatal(err)
	}
	if page, err := v.SearchMemories(ctx, "oldnote", 20, ""); err != nil || page.Total != 0 {
		t.Fatalf("replaced note remained searchable: %#v, %v", page, err)
	}
	page, err := v.SearchMemories(ctx, "iv2", 20, "")
	if err != nil || page.Total != 1 || len(page.Items) != 1 ||
		matchedExcerptText(page.Items[0].Excerpt) != "IV2" {
		t.Fatalf("updated note fragment/excerpt = %#v, %v", page, err)
	}
	clearNote := ""
	if _, err := v.ResolveUnderstandingUserNote(ctx, memory.ID, &clearNote); err != nil {
		t.Fatal(err)
	}
	if page, err := v.SearchMemories(ctx, "newnote", 20, ""); err != nil || page.Total != 0 {
		t.Fatalf("cleared note remained searchable: %#v, %v", page, err)
	}
	if _, err := v.ResolveUnderstandingUserNote(ctx, memory.ID, &newNote); err != nil {
		t.Fatal(err)
	}

	if err := dropSearchProjection(v.db); err != nil {
		t.Fatal(err)
	}
	v = reopenSearchVault(t, ctx, root, v)
	page, err = v.SearchMemories(ctx, "iv2", 20, "")
	if err != nil || page.Total != 1 || len(page.Items) != 1 ||
		matchedExcerptText(page.Items[0].Excerpt) != "IV2" {
		t.Fatalf("backfilled note fragment after reopen = %#v, %v", page, err)
	}

	if err := os.WriteFile(
		v.blobPath(memory.Blob.Ref),
		[]byte("unavailable original bytes"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := v.db.Exec(`DROP TABLE memory_search_fragments`); err != nil {
		t.Fatal(err)
	}
	v = reopenSearchVault(t, ctx, root, v)
	page, err = v.SearchMemories(ctx, "anton", 20, "")
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("existing fragment projection after reopen = %#v, %v", page, err)
	}
	if err := v.Delete(ctx, memory.ID); err != nil {
		t.Fatal(err)
	}
	page, err = v.SearchMemories(ctx, "anton", 20, "")
	if err != nil || page.Total != 0 || page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("deleted fragment result = %#v, %v", page, err)
	}
}

func matchedExcerptText(parts []SearchExcerptPart) string {
	var matched strings.Builder
	for _, part := range parts {
		if strings.ContainsAny(part.Text, "\x01\x02") {
			return "marker-leak"
		}
		if part.Match {
			matched.WriteString(part.Text)
		}
	}
	return matched.String()
}

func TestFragmentIndexFailureRollsBackMutations(t *testing.T) {
	for _, mutation := range []string{"import", "note", "delete"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			v := openInitializedSearchVault(t, ctx, root)
			memory := putSearchFixture(t, ctx, v, "microCIV2backup", ImportContext{
				OriginalFilename: "opaque.txt",
			})
			oldNote := "previous unique note"
			if _, err := v.ResolveUnderstandingUserNote(ctx, memory.ID, &oldNote); err != nil {
				t.Fatal(err)
			}
			if _, err := v.db.Exec(`DROP TABLE memory_search_fragments`); err != nil {
				t.Fatal(err)
			}
			var err error
			switch mutation {
			case "import":
				_, err = v.Put(
					ctx,
					Import{
						Content:       strings.NewReader("replacement fragment source"),
						MediaTypeHint: "text/plain",
						Context:       ImportContext{OriginalFilename: "replacement.txt"},
					},
				)
			case "note":
				note := "replaced note"
				_, err = v.ResolveUnderstandingUserNote(ctx, memory.ID, &note)
			case "delete":
				err = v.Delete(ctx, memory.ID)
			}
			if err == nil {
				t.Fatal("mutation committed without its fragment index")
			}
			v = reopenSearchVault(t, ctx, root, v)
			page, err := v.SearchMemories(ctx, "civ2 previous", 50, "")
			if err != nil || page.Total != 1 || len(page.Items) != 1 ||
				page.Items[0].Memory.ID != memory.ID {
				t.Fatalf("original body/note did not survive failed mutation: %#v, %v", page, err)
			}
			page, err = v.SearchMemories(ctx, "replacement", 50, "")
			if err != nil || page.Total != 0 {
				t.Fatalf("failed import leaked searchable content: %#v, %v", page, err)
			}
		})
	}
}

func TestFragmentNoteFailurePreservesBothIndexes(t *testing.T) {
	ctx := context.Background()
	v := openInitializedSearchVault(t, ctx, t.TempDir())
	memory := putSearchFixture(
		t,
		ctx,
		v,
		"original source bytes",
		ImportContext{OriginalFilename: "original.txt"},
	)
	oldNote := "microOldneedleRecord"
	if _, err := v.ResolveUnderstandingUserNote(ctx, memory.ID, &oldNote); err != nil {
		t.Fatal(err)
	}
	var trigger string
	if err := v.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name = 'memory_search_note_update'`).
		Scan(&trigger); err != nil {
		t.Fatal(err)
	}
	if _, err := v.db.Exec(`DROP TRIGGER memory_search_note_update`); err != nil {
		t.Fatal(err)
	}
	// Abort after the real update trigger has replaced both indexes, not before it runs.
	trigger = strings.TrimSuffix(
		strings.TrimSpace(trigger),
		"END",
	) + `SELECT RAISE(ABORT, 'injected late index failure'); END`
	if _, err := v.db.Exec(trigger); err != nil {
		t.Fatal(err)
	}
	newNote := "microNewneedleRecord"
	if _, err := v.ResolveUnderstandingUserNote(ctx, memory.ID, &newNote); err == nil {
		t.Fatal("note update ignored injected late failure")
	}
	for query, want := range map[string]int64{"oldneedle": 1, "microOldneedleRecord": 1, "newneedle": 0} {
		page, err := v.SearchMemories(ctx, query, 50, "")
		if err != nil || page.Total != want {
			t.Fatalf("query %q after failed note update: %#v, %v", query, page, err)
		}
	}
	var saved string
	if err := v.db.QueryRow(`SELECT user_note FROM memory_understanding_notes WHERE memory_id = ?`, memory.ID.String()).
		Scan(&saved); err != nil ||
		saved != oldNote {
		t.Fatalf("authoritative note changed after failure: %q, %v", saved, err)
	}
}
