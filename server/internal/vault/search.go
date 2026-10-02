package vault

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalidSearchQuery = errors.New("search query must contain at least one word")
	ErrInvalidSearchLimit = errors.New("search limit must be positive")
)

func (v *Vault) initializeSearch(ctx context.Context) error {
	tx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Memory search initialization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := initializeSearchIndexes(ctx, tx); err != nil {
		return fmt.Errorf("create Memory search indexes: %w", err)
	}
	for _, trigger := range []struct {
		name string
		sql  string
	}{
		{
			name: "memory_search_memory_delete",
			sql: `CREATE TRIGGER IF NOT EXISTS memory_search_memory_delete
				AFTER DELETE ON memories BEGIN
					` + deleteFragmentsSQL + ` WHERE rowid = OLD.rowid;
					DELETE FROM memory_search WHERE rowid = OLD.rowid;
				END`,
		},
		{
			name: "memory_search_note_insert",
			sql: `CREATE TRIGGER IF NOT EXISTS memory_search_note_insert
				AFTER INSERT ON memory_understanding_notes BEGIN
					` + deleteFragmentsSQL + ` WHERE rowid = (SELECT rowid FROM memories WHERE id = NEW.memory_id);
					UPDATE memory_search
					SET note = replace(replace(NEW.user_note, char(1), ' '), char(2), ' ')
					WHERE rowid = (SELECT rowid FROM memories WHERE id = NEW.memory_id);
					` + insertFragmentsSQL + ` WHERE rowid = (SELECT rowid FROM memories WHERE id = NEW.memory_id);
				END`,
		},
		{
			name: "memory_search_note_update",
			sql: `CREATE TRIGGER IF NOT EXISTS memory_search_note_update
				AFTER UPDATE OF user_note ON memory_understanding_notes BEGIN
					` + deleteFragmentsSQL + ` WHERE rowid = (SELECT rowid FROM memories WHERE id = NEW.memory_id);
					UPDATE memory_search
					SET note = replace(replace(NEW.user_note, char(1), ' '), char(2), ' ')
					WHERE rowid = (SELECT rowid FROM memories WHERE id = NEW.memory_id);
					` + insertFragmentsSQL + ` WHERE rowid = (SELECT rowid FROM memories WHERE id = NEW.memory_id);
				END`,
		},
		{
			name: "memory_search_note_delete",
			sql: `CREATE TRIGGER IF NOT EXISTS memory_search_note_delete
				AFTER DELETE ON memory_understanding_notes BEGIN
					` + deleteFragmentsSQL + ` WHERE rowid = (SELECT rowid FROM memories WHERE id = OLD.memory_id);
					UPDATE memory_search SET note = ''
					WHERE rowid = (SELECT rowid FROM memories WHERE id = OLD.memory_id);
					` + insertFragmentsSQL + ` WHERE rowid = (SELECT rowid FROM memories WHERE id = OLD.memory_id);
				END`,
		},
	} {
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS `+trigger.name); err != nil {
			return fmt.Errorf("replace Memory search trigger %s: %w", trigger.name, err)
		}
		if _, err := tx.ExecContext(ctx, trigger.sql); err != nil {
			return fmt.Errorf("create Memory search trigger %s: %w", trigger.name, err)
		}
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT memories.id, memories.blob_hash, memories.original_filename,
			memories.relative_path, memories.full_path,
			memories.filesystem_created_at, memories.filesystem_modified_at,
			memories.media_type, memories.byte_size, memories.imported_at,
			memories.rowid, COALESCE(memory_understanding_notes.user_note, '')
		FROM memories
		LEFT JOIN memory_understanding_notes
			ON memory_understanding_notes.memory_id = memories.id
		WHERE NOT EXISTS (
			SELECT 1 FROM memory_search WHERE memory_search.rowid = memories.rowid
		)
		ORDER BY memories.rowid
	`)
	if err != nil {
		return fmt.Errorf("read Memories missing from search index: %w", err)
	}
	type pendingMemory struct {
		memory Memory
		rowid  int64
		note   string
	}
	var pending []pendingMemory
	for rows.Next() {
		var row pendingMemory
		memory, scanErr := scanMemory(rows, &row.rowid, &row.note)
		if scanErr != nil {
			_ = rows.Close()
			return fmt.Errorf("read Memory for search backfill: %w", scanErr)
		}
		row.memory = memory
		pending = append(pending, row)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("read Memories missing from search index: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close Memory search backfill rows: %w", err)
	}

	for _, row := range pending {
		body, err := v.searchOriginalBody(row.memory)
		if err != nil {
			return fmt.Errorf("backfill Memory %s original body: %w", row.memory.ID, err)
		}
		if err := insertSearchRow(ctx, tx, row.rowid, row.memory, body, row.note); err != nil {
			return fmt.Errorf("backfill Memory %s search projection: %w", row.memory.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Memory search initialization: %w", err)
	}
	return nil
}

func (v *Vault) searchOriginalBody(memory Memory) (string, error) {
	mediaType := memory.Blob.MediaType
	if len(mediaType) < len("text/") || !strings.EqualFold(mediaType[:len("text/")], "text/") {
		return "", nil
	}
	body, err := readVerifiedBlob(
		v.blobPath(memory.Blob.Ref),
		memory.Blob.Ref,
		memory.Blob.ByteSize,
	)
	if err != nil {
		return "", fmt.Errorf("read verified original Blob: %w", err)
	}
	if !utf8.ValidString(body) || strings.IndexByte(body, 0) >= 0 {
		return "", nil
	}
	return body, nil
}

func (v *Vault) insertSearchMemory(
	ctx context.Context,
	tx *sql.Tx,
	memory Memory,
	body string,
) error {
	var rowid int64
	var note string
	if err := tx.QueryRowContext(ctx, `
		SELECT memories.rowid, COALESCE(memory_understanding_notes.user_note, '')
		FROM memories
		LEFT JOIN memory_understanding_notes
			ON memory_understanding_notes.memory_id = memories.id
		WHERE memories.id = ?
	`, memory.ID.String()).Scan(&rowid, &note); err != nil {
		return fmt.Errorf("read inserted Memory search metadata: %w", err)
	}
	if err := insertSearchRow(ctx, tx, rowid, memory, body, note); err != nil {
		return fmt.Errorf("insert Memory search projection: %w", err)
	}
	return nil
}

func insertSearchRow(
	ctx context.Context,
	tx *sql.Tx,
	rowid int64,
	memory Memory,
	body string,
	note string,
) error {
	paths := memory.ImportContext.RelativePath
	if paths != "" && memory.ImportContext.FullPath != "" {
		paths += "\n"
	}
	paths += memory.ImportContext.FullPath
	const normalize = "replace(replace(?, char(1), ' '), char(2), ' ')"
	_, err := tx.ExecContext(ctx, `
		INSERT INTO memory_search (rowid, filename, paths, body, note)
		VALUES (?, `+normalize+`, `+normalize+`, `+normalize+`, `+normalize+`)
	`, rowid, memory.ImportContext.OriginalFilename, paths, body, note)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, insertFragmentsSQL+` WHERE rowid = ?`, rowid)
	return err
}

type SearchPlan struct {
	Query string
	Terms []string
}

type SearchExcerptPart struct {
	Text  string
	Match bool
}

type SearchHit struct {
	Memory  Memory
	Excerpt []SearchExcerptPart
}

type SearchPage struct {
	QueryPlan SearchPlan
	Total     int64
	Items     []SearchHit
}

func (v *Vault) SearchMemories(ctx context.Context, query string, limit int64) (SearchPage, error) {
	if limit <= 0 {
		return SearchPage{}, ErrInvalidSearchLimit
	}
	plan := compileSearchPlan(query)
	if len(plan.Terms) == 0 {
		return SearchPage{}, ErrInvalidSearchQuery
	}

	tx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		return SearchPage{}, fmt.Errorf("begin Memory search: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	page := SearchPage{QueryPlan: plan, Items: make([]SearchHit, 0)}
	candidates, args := searchCandidates(plan.Terms)
	if err := tx.QueryRowContext(ctx, candidates+`SELECT count(*) FROM matches`, args...).
		Scan(&page.Total); err != nil {
		return SearchPage{}, fmt.Errorf("count Memory search matches: %w", err)
	}

	resultQuery, resultArgs := searchResultQuery(plan.Terms, limit)
	args = append(args, resultArgs...)
	rows, err := tx.QueryContext(ctx, candidates+resultQuery, args...)
	if err != nil {
		return SearchPage{}, fmt.Errorf("retrieve Memory search matches: %w", err)
	}
	for rows.Next() {
		var bodySnippet, fragmentBody, noteSnippet, fragmentNote, shortBody, shortNote string
		memory, scanErr := scanMemory(rows,
			&bodySnippet, &fragmentBody, &noteSnippet, &fragmentNote, &shortBody, &shortNote)
		if scanErr != nil {
			_ = rows.Close()
			return SearchPage{}, fmt.Errorf("read Memory search result: %w", scanErr)
		}
		excerpt := make([]SearchExcerptPart, 0)
		for _, snippet := range []string{bodySnippet, fragmentBody, shortBody, noteSnippet, fragmentNote, shortNote} {
			if strings.Contains(snippet, "\x01") {
				excerpt = highlightFragments(splitSearchExcerpt(snippet), plan.Terms)
				break
			}
		}
		page.Items = append(page.Items, SearchHit{Memory: memory, Excerpt: excerpt})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return SearchPage{}, fmt.Errorf("read Memory search results: %w", err)
	}
	if err := rows.Close(); err != nil {
		return SearchPage{}, fmt.Errorf("close Memory search results: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return SearchPage{}, fmt.Errorf("commit Memory search: %w", err)
	}
	return page, nil
}

func compileSearchPlan(query string) SearchPlan {
	plan := SearchPlan{Query: strings.TrimSpace(query), Terms: make([]string, 0)}
	var term strings.Builder
	hasWord := false
	flush := func() {
		if term.Len() > 0 && hasWord {
			plan.Terms = append(plan.Terms, strings.ToLower(term.String()))
		}
		term.Reset()
		hasWord = false
	}
	for _, r := range plan.Query {
		switch {
		case unicode.IsLetter(r), unicode.IsNumber(r), unicode.IsMark(r):
			term.WriteRune(r)
			if unicode.IsLetter(r) || unicode.IsNumber(r) {
				hasWord = true
			}
		default:
			flush()
		}
	}
	flush()
	return plan
}

func splitSearchExcerpt(snippet string) []SearchExcerptPart {
	parts := make([]SearchExcerptPart, 0, 3)
	start, matched := 0, false
	for i := range snippet {
		switch snippet[i] {
		case 1, 2:
			if i > start {
				parts = append(parts, SearchExcerptPart{Text: snippet[start:i], Match: matched})
			}
			matched = snippet[i] == 1
			start = i + 1
		}
	}
	if start < len(snippet) {
		parts = append(parts, SearchExcerptPart{Text: snippet[start:], Match: matched})
	}
	return parts
}
