package vault

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

func (v *Vault) initializeSearchDerived(ctx context.Context, tx *sql.Tx, rebuilt bool) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS memory_search_derived (
		memory_id TEXT PRIMARY KEY NOT NULL,
		run_id TEXT NOT NULL,
		FOREIGN KEY (memory_id) REFERENCES memories(id) ON DELETE CASCADE,
		FOREIGN KEY (run_id, memory_id)
			REFERENCES understanding_runs(id, memory_id) ON DELETE CASCADE
	)`); err != nil {
		return fmt.Errorf("create active search marker table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TRIGGER IF NOT EXISTS memory_search_active_run_delete
		AFTER DELETE ON active_understanding_runs BEGIN
			`+deleteFragmentsSQL+` WHERE rowid = (SELECT rowid FROM memories WHERE id = OLD.memory_id);
			UPDATE memory_search SET derived = ''
			WHERE rowid = (SELECT rowid FROM memories WHERE id = OLD.memory_id);
			`+insertFragmentsSQL+` WHERE rowid = (SELECT rowid FROM memories WHERE id = OLD.memory_id);
			DELETE FROM memory_search_derived WHERE memory_id = OLD.memory_id;
		END`); err != nil {
		return fmt.Errorf("create active Run search cleanup trigger: %w", err)
	}
	if rebuilt {
		if _, err := tx.ExecContext(ctx, `DELETE FROM memory_search_derived`); err != nil {
			return fmt.Errorf("reset active search markers: %w", err)
		}
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT memories.id, active.run_id, memories.rowid
		FROM active_understanding_runs active
		JOIN memories ON memories.id = active.memory_id
		LEFT JOIN memory_search_derived markers
			ON markers.memory_id = active.memory_id AND markers.run_id = active.run_id
		WHERE markers.memory_id IS NULL
		ORDER BY memories.id
	`)
	if err != nil {
		return fmt.Errorf("read active Runs missing Derived Content projection: %w", err)
	}
	type activeRun struct {
		memoryID string
		runID    string
		rowid    int64
	}
	var pending []activeRun
	for rows.Next() {
		var run activeRun
		if err := rows.Scan(&run.memoryID, &run.runID, &run.rowid); err != nil {
			_ = rows.Close()
			return fmt.Errorf("read active Run for Derived Content backfill: %w", err)
		}
		pending = append(pending, run)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("read active Runs missing Derived Content projection: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close active Run backfill rows: %w", err)
	}

	for _, run := range pending {
		derived, err := v.readSearchDerivedRun(ctx, tx, run.memoryID, run.runID)
		if err != nil {
			return fmt.Errorf(
				"backfill active Run %s for Memory %s: %w",
				run.runID,
				run.memoryID,
				err,
			)
		}
		if err := writeSearchDerived(ctx, tx, run.rowid, derived); err != nil {
			return fmt.Errorf("write Derived Content for Memory %s: %w", run.memoryID, err)
		}
		if err := storeSearchDerivedMarker(ctx, tx, run.memoryID, run.runID); err != nil {
			return fmt.Errorf("store Derived Content marker for Memory %s: %w", run.memoryID, err)
		}
	}
	return nil
}

func (v *Vault) setActiveSearchDerived(
	ctx context.Context,
	tx *sql.Tx,
	memoryID, runID uuid.UUID,
	artifacts []DerivedContent,
) error {
	derived := searchDerivedText(artifacts)
	var rowid int64
	if err := tx.QueryRowContext(ctx,
		`SELECT rowid FROM memories WHERE id = ?`, memoryID.String()).Scan(&rowid); err != nil {
		return fmt.Errorf("read Memory search row: %w", err)
	}
	if err := writeSearchDerived(ctx, tx, rowid, derived); err != nil {
		return err
	}
	return storeSearchDerivedMarker(ctx, tx, memoryID.String(), runID.String())
}

func storeSearchDerivedMarker(ctx context.Context, tx *sql.Tx, memoryID, runID string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO memory_search_derived (memory_id, run_id) VALUES (?, ?)
		ON CONFLICT(memory_id) DO UPDATE SET run_id = excluded.run_id
	`, memoryID, runID)
	return err
}

func writeSearchDerived(ctx context.Context, tx *sql.Tx, rowid int64, derived string) error {
	if _, err := tx.ExecContext(ctx, deleteFragmentsSQL+` WHERE rowid = ?`, rowid); err != nil {
		return fmt.Errorf("remove prior Derived Content fragments: %w", err)
	}
	result, err := tx.ExecContext(
		ctx,
		`UPDATE memory_search SET derived = ? WHERE rowid = ?`,
		normalizeSearchDerivedText(derived),
		rowid,
	)
	if err != nil {
		return fmt.Errorf("update Derived Content search projection: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check Derived Content projection update: %w", err)
	}
	if updated != 1 {
		return fmt.Errorf("Memory search row %d is missing", rowid)
	}
	if _, err := tx.ExecContext(ctx, insertFragmentsSQL+` WHERE rowid = ?`, rowid); err != nil {
		return fmt.Errorf("insert Derived Content fragments: %w", err)
	}
	return nil
}

type searchDerivedArtifact struct {
	ref       string
	mediaType string
	byteSize  int64
}

func (v *Vault) readSearchDerivedRun(
	ctx context.Context,
	tx *sql.Tx,
	memoryID, runID string,
) (string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT blob_hash, content_type, byte_size
		FROM understanding_artifacts
		WHERE memory_id = ? AND run_id = ?
		ORDER BY ordinal
	`, memoryID, runID)
	if err != nil {
		return "", fmt.Errorf("read active Run artifacts: %w", err)
	}
	var artifacts []searchDerivedArtifact
	for rows.Next() {
		var artifact searchDerivedArtifact
		if err := rows.Scan(&artifact.ref, &artifact.mediaType, &artifact.byteSize); err != nil {
			_ = rows.Close()
			return "", fmt.Errorf("read active Run artifact: %w", err)
		}
		artifacts = append(artifacts, artifact)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", fmt.Errorf("read active Run artifacts: %w", err)
	}
	if err := rows.Close(); err != nil {
		return "", fmt.Errorf("close active Run artifact rows: %w", err)
	}

	var documents []string
	for _, artifact := range artifacts {
		kind := searchDerivedArtifactKind(artifact.mediaType)
		if kind == "" {
			continue
		}
		ref, err := ParseBlobref(artifact.ref)
		if err != nil {
			return "", fmt.Errorf(
				"parse supported active Run artifact Blobref %q: %w",
				artifact.ref,
				err,
			)
		}
		content, err := readVerifiedBlob(v.derivedBlobPath(ref), ref, artifact.byteSize)
		if err != nil {
			return "", fmt.Errorf("read verified %s artifact %s: %w", kind, ref, err)
		}
		text := searchDerivedArtifactText(kind, content)
		if text != "" {
			documents = append(documents, text)
		}
	}
	return strings.Join(documents, "\n"), nil
}

func searchDerivedArtifactKind(mediaType string) string {
	parsed, _, err := mime.ParseMediaType(mediaType)
	if err != nil {
		return ""
	}
	parsed = strings.ToLower(parsed)
	if strings.HasPrefix(parsed, "text/") {
		return "text"
	}
	if parsed == "application/json" {
		return "json"
	}
	return ""
}

func searchDerivedText(artifacts []DerivedContent) string {
	var documents []string
	for _, artifact := range artifacts {
		kind := searchDerivedArtifactKind(artifact.Blob.MediaType)
		if kind == "" {
			continue
		}
		text := searchDerivedArtifactText(kind, artifact.Content)
		if text != "" {
			documents = append(documents, text)
		}
	}
	return strings.Join(documents, "\n")
}

func searchDerivedArtifactText(kind, content string) string {
	if !utf8.ValidString(content) || strings.TrimSpace(content) == "" {
		return ""
	}
	if kind == "text" {
		return content
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.UseNumber()
	var value any
	// Plugin artifacts are literal text; JSON that cannot be interpreted is unsupported search input, not an invalid Run.
	if err := decoder.Decode(&value); err != nil {
		return ""
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ""
	}
	values := make([]string, 0)
	collectSearchJSONValues(value, &values)
	return strings.Join(values, "\n")
}

func collectSearchJSONValues(value any, values *[]string) {
	// ponytail: scalar leaves omit reserved processing keys; use schema-specific extraction if formats need finer semantics.
	switch value := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if searchJSONMetadataKey(key) {
				continue
			}
			collectSearchJSONValues(value[key], values)
		}
	case []any:
		for _, item := range value {
			collectSearchJSONValues(item, values)
		}
	case string:
		if strings.TrimSpace(value) != "" {
			*values = append(*values, value)
		}
	case json.Number:
		*values = append(*values, value.String())
	case bool:
		if value {
			*values = append(*values, "true")
		} else {
			*values = append(*values, "false")
		}
	}
}

func searchJSONMetadataKey(key string) bool {
	switch strings.ToLower(key) {
	case "schema", "provenance", "scope", "metadata", "warning", "warnings",
		"log", "logs", "statistics", "usage", "cost_estimate",
		"protocol_version", "plugin_version", "processing", "processing_metadata":
		return true
	default:
		return false
	}
}

var searchDerivedNormalizer = strings.NewReplacer("\x00", " ", "\x01", " ", "\x02", " ")

func normalizeSearchDerivedText(text string) string {
	if !strings.ContainsAny(text, "\x00\x01\x02") {
		return text
	}
	return searchDerivedNormalizer.Replace(text)
}
