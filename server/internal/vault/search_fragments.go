package vault

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"modernc.org/sqlite"
)

const fragmentColumns = "rowid, filename, paths, body, note"

const deleteFragmentsSQL = `INSERT INTO memory_search_fragments (memory_search_fragments, ` + fragmentColumns + `)
	SELECT 'delete', ` + fragmentColumns + ` FROM memory_search`
const insertFragmentsSQL = `INSERT INTO memory_search_fragments (` + fragmentColumns + `)
	SELECT ` + fragmentColumns + ` FROM memory_search`

func init() {
	sqlite.MustRegisterDeterministicScalarFunction("memory_search_contains", 2,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			text, _ := args[0].(string)
			term, _ := args[1].(string)
			start, _ := foldedFragment(text, term)
			if start >= 0 {
				return int64(1), nil
			}
			return int64(0), nil
		})
	sqlite.MustRegisterDeterministicScalarFunction("memory_search_short_excerpt", 2,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			text, _ := args[0].(string)
			term, _ := args[1].(string)
			start, end := foldedFragment(text, term)
			if start < 0 {
				return nil, nil
			}
			left, right := start, end
			for i := 0; i < 80 && left > 0; i++ {
				_, size := utf8.DecodeLastRuneInString(text[:left])
				left -= size
			}
			for i := 0; i < 80 && right < len(text); i++ {
				_, size := utf8.DecodeRuneInString(text[right:])
				right += size
			}
			var snippet strings.Builder
			snippet.Grow(right - left + 8)
			if left > 0 {
				snippet.WriteString("…")
			}
			snippet.WriteString(text[left:start])
			snippet.WriteByte(1)
			snippet.WriteString(text[start:end])
			snippet.WriteByte(2)
			snippet.WriteString(text[end:right])
			if right < len(text) {
				snippet.WriteString("…")
			}
			return snippet.String(), nil
		})
}

func initializeSearchIndexes(ctx context.Context, tx *sql.Tx) error {
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema
		WHERE name IN ('memory_search', 'memory_search_fragments')`).Scan(&existing); err != nil {
		return err
	}
	for _, statement := range []string{
		`CREATE VIRTUAL TABLE IF NOT EXISTS memory_search USING fts5(
			filename, paths, body, note, tokenize = 'porter unicode61')`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS memory_search_fragments USING fts5(
			filename, paths, body, note, content='memory_search', tokenize='trigram')`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create search projection: %w", err)
		}
	}
	if existing != 2 {
		// Reuse stored projection text; never reread original Blobs to add this index.
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO memory_search_fragments(memory_search_fragments) VALUES ('delete-all')`,
		); err != nil {
			return fmt.Errorf("reset fragment index: %w", err)
		}
		_, err := tx.ExecContext(ctx, insertFragmentsSQL)
		if err != nil {
			return fmt.Errorf("backfill fragment index: %w", err)
		}
	}
	return nil
}

func searchCandidates(terms []string) (string, []any) {
	var query strings.Builder
	args := make([]any, 0, len(terms)*5)
	query.WriteString("WITH ")
	for i, term := range terms {
		if i > 0 {
			query.WriteString(", ")
		}
		query.WriteString(
			"term" + strconv.Itoa(
				i,
			) + " AS (SELECT rowid FROM memory_search WHERE memory_search MATCH ? UNION ",
		)
		args = append(args, `"`+term+`"`)
		if utf8.RuneCountInString(term) >= 3 {
			query.WriteString(
				"SELECT rowid FROM memory_search_fragments WHERE memory_search_fragments MATCH ?)",
			)
			args = append(args, `"`+term+`"`)
		} else {
			// ponytail: one/two-rune fragments scan indexed text; measure before adding a short-gram index.
			query.WriteString(`SELECT rowid FROM memory_search WHERE
				memory_search_contains(filename, ?) OR memory_search_contains(paths, ?) OR
				memory_search_contains(body, ?) OR memory_search_contains(note, ?))`)
			args = append(args, term, term, term, term)
		}
	}
	query.WriteString(", matches AS (")
	for i := range terms {
		if i > 0 {
			query.WriteString(" INTERSECT ")
		}
		query.WriteString("SELECT rowid FROM term" + strconv.Itoa(i))
	}
	query.WriteString(") ")
	return query.String(), args
}

func searchResultQuery(terms []string, limit int64) (string, []any) {
	var longTerms []string
	for _, term := range terms {
		if utf8.RuneCountInString(term) >= 3 {
			longTerms = append(longTerms, term)
		}
	}
	fragmentCondition := "0"
	args := []any{ftsTerms(terms, "AND"), ftsTerms(terms, "OR"), limit, ftsTerms(terms, "OR")}
	if len(longTerms) > 0 {
		fragmentCondition = "memory_search_fragments MATCH ?"
		args = append(args, ftsTerms(longTerms, "OR"))
	}
	shortBody, shortNote := "''", "''"
	if len(longTerms) != len(terms) {
		shortBody = "CASE WHEN instr(COALESCE(words.body, ''), char(1)) OR instr(COALESCE(fragments.body, ''), char(1)) THEN '' ELSE " +
			shortExcerptSQL(
				"memory_search.body",
				terms,
				&args,
			) + " END"
		shortNote = "CASE WHEN instr(COALESCE(words.note, ''), char(1)) OR instr(COALESCE(fragments.note, ''), char(1)) THEN '' ELSE " +
			shortExcerptSQL(
				"memory_search.note",
				terms,
				&args,
			) + " END"
	}
	query := `, precise AS MATERIALIZED (SELECT rowid FROM memory_search WHERE memory_search MATCH ?),
		word_ranks AS MATERIALIZED (
			SELECT rowid, bm25(memory_search) AS score FROM memory_search WHERE memory_search MATCH ?),
		selected AS MATERIALIZED (
			SELECT matches.rowid, precise.rowid IS NULL AS tier,
				COALESCE(word_ranks.score, 0) AS score, memories.id
			FROM matches JOIN memories ON memories.rowid = matches.rowid
			LEFT JOIN precise ON precise.rowid = matches.rowid
			LEFT JOIN word_ranks ON word_ranks.rowid = matches.rowid
			ORDER BY tier, score, memories.id LIMIT ?),
		words AS MATERIALIZED (
			SELECT rowid,
				snippet(memory_search, 2, char(1), char(2), '…', 32) AS body,
				snippet(memory_search, 3, char(1), char(2), '…', 32) AS note
			FROM memory_search WHERE memory_search MATCH ? AND rowid IN (SELECT rowid FROM selected)),
		fragments AS MATERIALIZED (
			SELECT rowid,
				snippet(memory_search_fragments, 2, char(1), char(2), '…', 64) AS body,
				snippet(memory_search_fragments, 3, char(1), char(2), '…', 64) AS note
			FROM memory_search_fragments WHERE ` + fragmentCondition + ` AND rowid IN (SELECT rowid FROM selected))
		SELECT memories.id, memories.blob_hash, memories.original_filename,
			memories.relative_path, memories.full_path,
			memories.filesystem_created_at, memories.filesystem_modified_at,
			memories.media_type, memories.byte_size, memories.imported_at,
			COALESCE(words.body, ''), COALESCE(fragments.body, ''),
			COALESCE(words.note, ''), COALESCE(fragments.note, ''),
			` + shortBody + `, ` + shortNote + `
		FROM selected JOIN memories ON memories.rowid = selected.rowid
		JOIN memory_search ON memory_search.rowid = selected.rowid
		LEFT JOIN words ON words.rowid = selected.rowid
		LEFT JOIN fragments ON fragments.rowid = selected.rowid
		ORDER BY selected.tier, selected.score, selected.id`
	return query, args
}

func ftsTerms(terms []string, operator string) string {
	return `"` + strings.Join(terms, `" `+operator+` "`) + `"`
}

func shortExcerptSQL(column string, terms []string, args *[]any) string {
	var query strings.Builder
	query.WriteString("COALESCE(")
	for _, term := range terms {
		if utf8.RuneCountInString(term) < 3 {
			query.WriteString("memory_search_short_excerpt(" + column + ", ?), ")
			*args = append(*args, term)
		}
	}
	query.WriteString("'')")
	return query.String()
}

func foldedFragment(text, term string) (int, int) {
	if term == "" {
		return -1, -1
	}
	for start := range text {
		if end, ok := foldedPrefix(text[start:], term); ok {
			return start, start + end
		}
	}
	return -1, -1
}

func foldedPrefix(text, term string) (int, bool) {
	end := 0
	for index, want := range term {
		if end == len(text) {
			return 0, false
		}
		_, size := utf8.DecodeRuneInString(text[end:])
		if !strings.EqualFold(text[end:end+size], term[index:index+utf8.RuneLen(want)]) {
			return 0, false
		}
		end += size
	}
	return end, true
}

func highlightFragments(parts []SearchExcerptPart, terms []string) []SearchExcerptPart {
	result := make([]SearchExcerptPart, 0, len(parts))
	for _, part := range parts {
		if part.Match {
			result = append(result, part)
			continue
		}
		start, matchEnd, matched := 0, 0, false
		for i := range part.Text {
			for _, term := range terms {
				if end, ok := foldedPrefix(part.Text[i:], term); ok && i+end > matchEnd {
					matchEnd = i + end
				}
			}
			if nextMatched := i < matchEnd; nextMatched != matched {
				if i > start {
					result = append(
						result,
						SearchExcerptPart{Text: part.Text[start:i], Match: matched},
					)
				}
				start, matched = i, nextMatched
			}
		}
		if start < len(part.Text) {
			result = append(result, SearchExcerptPart{Text: part.Text[start:], Match: matched})
		}
	}
	return result
}
