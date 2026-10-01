package vault

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	understandingStatusQueued  = "queued"
	understandingStatusRunning = "running"
	understandingStatusDone    = "done"
	understandingStatusFailed  = "failed"
	understandingTimeLayout    = "2006-01-02T15:04:05.000000000Z07:00"
)

var (
	ErrNoUnderstandingWork            = errors.New("no queued understanding work")
	ErrUnderstandingAttemptNotRunning = errors.New("understanding attempt is not running")
)

type UnderstandingDiagnostics struct {
	Error    string
	Stdout   string
	Stderr   string
	ExitCode *int
}

type UnderstandingAttempt struct {
	ID          uuid.UUID
	MemoryID    uuid.UUID
	Status      string
	PluginID    string
	QueuedAt    time.Time
	StartedAt   *time.Time
	CompletedAt *time.Time
	Diagnostics *UnderstandingDiagnostics
}

type Statistics struct {
	Usage *TokenUsage `json:"usage,omitempty"`
}

type TokenUsage struct {
	InputTokens           *int64 `json:"input_tokens,omitempty"`
	CachedInputTokens     *int64 `json:"cached_input_tokens,omitempty"`
	CacheWriteInputTokens *int64 `json:"cache_write_input_tokens,omitempty"`
	OutputTokens          *int64 `json:"output_tokens,omitempty"`
	ReasoningOutputTokens *int64 `json:"reasoning_output_tokens,omitempty"`
	TotalTokens           *int64 `json:"total_tokens,omitempty"`
}

type CostEstimate struct {
	AmountUSD   float64 `json:"amount_usd"`
	Basis       string  `json:"basis"`
	PricingDate string  `json:"pricing_date,omitempty"`
	PricingURL  string  `json:"pricing_url,omitempty"`
}

type DerivedContent struct {
	ID         uuid.UUID
	Blob       BlobInfo
	Content    string
	Provenance json.RawMessage
	Scope      json.RawMessage
}

type UnderstandingRun struct {
	ID            uuid.UUID
	MemoryID      uuid.UUID
	AttemptID     uuid.UUID
	PluginID      string
	PluginVersion string
	SourceBlobref Blobref
	CreatedAt     time.Time
	CompletedAt   time.Time
	Warnings      []string
	Statistics    *Statistics
	CostEstimate  *CostEstimate
	Artifacts     []DerivedContent
}

type understandingRunReporting struct {
	Statistics   *Statistics   `json:"statistics,omitempty"`
	CostEstimate *CostEstimate `json:"cost_estimate,omitempty"`
}

type UnderstandingDetails struct {
	Status    string
	Attempt   *UnderstandingAttempt
	ActiveRun *UnderstandingRun
}

// RecoverUnderstanding queues never-attempted Memories and retries the latest
// failed or interrupted attempt once per startup.
func (v *Vault) RecoverUnderstanding(ctx context.Context) error {
	tx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin understanding recovery: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
		SELECT m.id
		FROM memories m
		WHERE (
			NOT EXISTS (
				SELECT 1 FROM understanding_attempts a WHERE a.memory_id = m.id
			) AND NOT EXISTS (
				SELECT 1 FROM active_understanding_runs r WHERE r.memory_id = m.id
			)
		) OR (
			SELECT a.status FROM understanding_attempts a
			WHERE a.memory_id = m.id
			ORDER BY a.queued_at DESC, a.id DESC LIMIT 1
		) IN ('failed', 'running')
		ORDER BY m.imported_at, m.id
	`)
	if err != nil {
		return fmt.Errorf("find Memories requiring understanding recovery: %w", err)
	}
	var memoryIDs []string
	for rows.Next() {
		var memoryID string
		if err := rows.Scan(&memoryID); err != nil {
			_ = rows.Close()
			return fmt.Errorf("read Memory requiring understanding recovery: %w", err)
		}
		memoryIDs = append(memoryIDs, memoryID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("read Memories requiring understanding recovery: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close understanding recovery rows: %w", err)
	}

	queuedAt := time.Now().UTC()
	for index, memoryID := range memoryIDs {
		createdAt := queuedAt.Add(time.Duration(index) * time.Nanosecond)
		_, err := tx.ExecContext(ctx, `
			INSERT INTO understanding_attempts (id, memory_id, status, queued_at)
			VALUES (?, ?, ?, ?)
		`, uuid.New().String(), memoryID, understandingStatusQueued,
			formatUnderstandingTime(createdAt))
		if err != nil {
			return fmt.Errorf("queue recovered understanding attempt: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit understanding recovery: %w", err)
	}
	return nil
}

func formatUnderstandingTime(value time.Time) string {
	return value.UTC().Format(understandingTimeLayout)
}

// ClaimUnderstanding atomically moves the oldest queued attempt to running.
func (v *Vault) ClaimUnderstanding(
	ctx context.Context,
) (Memory, UnderstandingAttempt, error) {
	tx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		return Memory{}, UnderstandingAttempt{}, fmt.Errorf("begin understanding claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	startedAt := time.Now().UTC()
	attempt, err := scanUnderstandingAttempt(tx.QueryRowContext(ctx, `
		UPDATE understanding_attempts
		SET status = ?, started_at = ?
		WHERE id = (
			SELECT id FROM understanding_attempts
			WHERE status = ? ORDER BY queued_at, id LIMIT 1
		) AND status = ?
		RETURNING id, memory_id, status, plugin_id, queued_at, started_at,
			completed_at, diagnostics_error, diagnostics_stdout, diagnostics_stderr,
			diagnostics_exit_code
	`, understandingStatusRunning, formatUnderstandingTime(startedAt),
		understandingStatusQueued, understandingStatusQueued))
	if errors.Is(err, sql.ErrNoRows) {
		return Memory{}, UnderstandingAttempt{}, ErrNoUnderstandingWork
	}
	if err != nil {
		return Memory{}, UnderstandingAttempt{}, fmt.Errorf(
			"claim queued understanding attempt: %w",
			err,
		)
	}
	memory, err := scanMemory(tx.QueryRowContext(
		ctx,
		selectMemorySQL+` WHERE id = ?`,
		attempt.MemoryID.String(),
	))
	if err != nil {
		return Memory{}, UnderstandingAttempt{}, err
	}
	if err := tx.Commit(); err != nil {
		return Memory{}, UnderstandingAttempt{}, fmt.Errorf("commit understanding claim: %w", err)
	}
	return memory, attempt, nil
}

func (v *Vault) SetUnderstandingPlugin(
	ctx context.Context,
	attemptID uuid.UUID,
	pluginID string,
) error {
	result, err := v.db.ExecContext(ctx, `
		UPDATE understanding_attempts SET plugin_id = ?
		WHERE id = ? AND status = ?
			AND id = (
				SELECT latest.id FROM understanding_attempts latest
				WHERE latest.memory_id = understanding_attempts.memory_id
				ORDER BY latest.queued_at DESC, latest.id DESC LIMIT 1
			)
	`, pluginID, attemptID.String(), understandingStatusRunning)
	if err != nil {
		return fmt.Errorf("record understanding plugin: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect understanding plugin update: %w", err)
	}
	if updated == 0 {
		return ErrUnderstandingAttemptNotRunning
	}
	return nil
}

func (v *Vault) FinishUnderstanding(
	ctx context.Context,
	attempt UnderstandingAttempt,
	runID uuid.UUID,
	pluginVersion string,
	artifacts []DerivedContent,
	warnings []string,
	statistics *Statistics,
	costEstimate *CostEstimate,
) error {
	if attempt.Status != understandingStatusRunning {
		return ErrUnderstandingAttemptNotRunning
	}

	checkTx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin understanding completion check: %w", err)
	}
	defer func() { _ = checkTx.Rollback() }()
	if _, _, err := runningUnderstandingAttempt(ctx, checkTx, attempt); err != nil {
		return err
	}
	if err := checkTx.Commit(); err != nil {
		return fmt.Errorf("finish understanding completion check: %w", err)
	}

	if warnings == nil {
		warnings = []string{}
	}
	warningsJSON, err := json.Marshal(warnings)
	if err != nil {
		return fmt.Errorf("encode understanding warnings: %w", err)
	}
	var reportingJSON []byte
	if statistics != nil || costEstimate != nil {
		reportingJSON, err = json.Marshal(understandingRunReporting{
			Statistics:   statistics,
			CostEstimate: costEstimate,
		})
		if err != nil {
			return fmt.Errorf("encode Understanding Run reporting: %w", err)
		}
	}
	storedArtifacts := make([]storedUnderstandingArtifact, len(artifacts))
	for index, artifact := range artifacts {
		storedArtifacts[index] = storedUnderstandingArtifact{
			id:             uuid.New(),
			ordinal:        index,
			content:        artifact.Content,
			contentType:    artifact.Blob.MediaType,
			provenanceJSON: rawJSONArgument(artifact.Provenance),
			scopeJSON:      rawJSONArgument(artifact.Scope),
			blob:           BlobInfo{},
		}
	}
	for index := range storedArtifacts {
		if err := ctx.Err(); err != nil {
			return err
		}
		blob, err := v.publishDerivedBlob(
			ctx,
			storedArtifacts[index].content,
			storedArtifacts[index].contentType,
		)
		if err != nil {
			return fmt.Errorf("publish derived Blob: %w", err)
		}
		storedArtifacts[index].blob = blob
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	tx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Understanding Run commit: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	memory, pluginID, err := runningUnderstandingAttempt(ctx, tx, attempt)

	if err != nil {
		return err
	}

	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO understanding_runs (
			id, memory_id, attempt_id, plugin_id, plugin_version, source_blobref,
			created_at, completed_at, warnings_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, runID.String(), memory.ID.String(), attempt.ID.String(), pluginID, pluginVersion,
		memory.Blob.Ref.String(), formatUnderstandingTime(now),
		formatUnderstandingTime(now), string(warningsJSON))
	if err != nil {
		return fmt.Errorf("insert Understanding Run: %w", err)
	}
	if len(reportingJSON) > 0 {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO understanding_run_statistics (run_id, statistics_json)
			VALUES (?, ?)
		`, runID.String(), string(reportingJSON))
		if err != nil {
			return fmt.Errorf("insert Understanding Run reporting: %w", err)
		}
	}
	for _, artifact := range storedArtifacts {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO understanding_artifacts (
				id, run_id, memory_id, ordinal, blob_hash, content_type,
				byte_size, provenance_json, scope_json
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, artifact.id.String(), runID.String(), memory.ID.String(), artifact.ordinal,
			artifact.blob.Ref.String(), artifact.blob.MediaType, artifact.blob.ByteSize,
			artifact.provenanceJSON, artifact.scopeJSON)
		if err != nil {
			return fmt.Errorf("insert derived artifact: %w", err)
		}
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO active_understanding_runs (memory_id, run_id) VALUES (?, ?)
		ON CONFLICT(memory_id) DO UPDATE SET run_id = excluded.run_id
	`, memory.ID.String(), runID.String())
	if err != nil {
		return fmt.Errorf("activate Understanding Run: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE understanding_attempts
		SET status = ?, completed_at = ?, diagnostics_error = NULL,
			diagnostics_stdout = NULL, diagnostics_stderr = NULL,
			diagnostics_exit_code = NULL
		WHERE id = ? AND memory_id = ? AND status = ?
			AND id = (
				SELECT latest.id FROM understanding_attempts latest
				WHERE latest.memory_id = understanding_attempts.memory_id
				ORDER BY latest.queued_at DESC, latest.id DESC LIMIT 1
			)
	`, understandingStatusDone, formatUnderstandingTime(now), attempt.ID.String(),
		memory.ID.String(), understandingStatusRunning)
	if err != nil {
		return fmt.Errorf("complete understanding attempt: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect understanding completion: %w", err)
	}
	if updated != 1 {
		return ErrUnderstandingAttemptNotRunning
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Understanding Run: %w", err)
	}
	return nil
}

func runningUnderstandingAttempt(
	ctx context.Context,
	tx *sql.Tx,
	attempt UnderstandingAttempt,
) (Memory, string, error) {
	memory, err := scanMemory(tx.QueryRowContext(
		ctx, selectMemorySQL+` WHERE id = ?`, attempt.MemoryID.String(),
	))

	if err != nil {
		return Memory{}, "", err
	}
	var pluginID, status, memoryID string
	var latest int

	if err := tx.QueryRowContext(ctx, `
		SELECT attempt.plugin_id, attempt.status, attempt.memory_id,
			attempt.id = (
				SELECT latest.id FROM understanding_attempts latest
				WHERE latest.memory_id = attempt.memory_id
				ORDER BY latest.queued_at DESC, latest.id DESC LIMIT 1
			)
		FROM understanding_attempts attempt WHERE attempt.id = ?
	`, attempt.ID.String()).Scan(&pluginID, &status, &memoryID, &latest); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Memory{}, "", ErrUnderstandingAttemptNotRunning
		}
		return Memory{}, "", fmt.Errorf("read understanding attempt for Run: %w", err)
	}
	if status != understandingStatusRunning || memoryID != attempt.MemoryID.String() ||
		latest != 1 {
		return Memory{}, "", ErrUnderstandingAttemptNotRunning
	}
	return memory, pluginID, nil
}

type storedUnderstandingArtifact struct {
	id             uuid.UUID
	ordinal        int
	content        string
	contentType    string
	provenanceJSON any
	scopeJSON      any
	blob           BlobInfo
}

func rawJSONArgument(value json.RawMessage) any {
	if value == nil {
		return nil
	}
	return string(value)
}

func (v *Vault) publishDerivedBlob(
	ctx context.Context,
	content string,
	mediaType string,
) (BlobInfo, error) {
	if err := ctx.Err(); err != nil {
		return BlobInfo{}, err
	}
	temporary, err := os.CreateTemp(v.uploadDir, ".derived-*")
	if err != nil {
		return BlobInfo{}, fmt.Errorf("create derived Blob staging file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()

	hash := sha256.New()
	byteSize, copyErr := io.Copy(io.MultiWriter(temporary, hash), strings.NewReader(content))
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if copyErr != nil {
		return BlobInfo{}, fmt.Errorf("stage derived Blob: %w", copyErr)
	}
	if syncErr != nil {
		return BlobInfo{}, fmt.Errorf("flush derived Blob staging file: %w", syncErr)
	}
	if closeErr != nil {
		return BlobInfo{}, fmt.Errorf("close derived Blob staging file: %w", closeErr)
	}
	if err := ctx.Err(); err != nil {
		return BlobInfo{}, err
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	ref := NewSHA256Blobref(digest)
	finalPath := v.derivedBlobPath(ref)
	if err := v.ensureBlobDirectory(finalPath); err != nil {
		return BlobInfo{}, fmt.Errorf("create derived Blob shard directory: %w", err)
	}
	if err := publishBlob(temporaryPath, finalPath, ref, byteSize); err != nil {
		return BlobInfo{}, fmt.Errorf("publish derived Blob: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return BlobInfo{}, err
	}
	return BlobInfo{Ref: ref, MediaType: mediaType, ByteSize: byteSize}, nil
}

func (v *Vault) FailUnderstanding(
	ctx context.Context,
	attemptID uuid.UUID,
	diagnostics UnderstandingDiagnostics,
) error {
	completedAt := formatUnderstandingTime(time.Now().UTC())
	result, err := v.db.ExecContext(ctx, `
		UPDATE understanding_attempts
		SET status = ?, completed_at = ?, diagnostics_error = ?,
			diagnostics_stdout = ?, diagnostics_stderr = ?, diagnostics_exit_code = ?
		WHERE id = ? AND status = ?
			AND id = (
				SELECT latest.id FROM understanding_attempts latest
				WHERE latest.memory_id = understanding_attempts.memory_id
				ORDER BY latest.queued_at DESC, latest.id DESC LIMIT 1
			)
	`, understandingStatusFailed, completedAt, diagnostics.Error, diagnostics.Stdout,
		diagnostics.Stderr, nullableInt(diagnostics.ExitCode), attemptID.String(),
		understandingStatusRunning)
	if err != nil {
		return fmt.Errorf("record failed understanding attempt: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect failed understanding attempt: %w", err)
	}
	if updated != 0 {
		return nil
	}
	var memoryID, status string
	err = v.db.QueryRowContext(ctx, `
		SELECT memory_id, status FROM understanding_attempts WHERE id = ?
	`, attemptID.String()).Scan(&memoryID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect failed understanding attempt: %w", err)
	}
	var exists int
	err = v.db.QueryRowContext(ctx, `SELECT 1 FROM memories WHERE id = ?`, memoryID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect failed attempt Memory: %w", err)
	}
	if status != understandingStatusRunning {
		return ErrUnderstandingAttemptNotRunning
	}
	return nil
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func (v *Vault) Understanding(
	ctx context.Context,
	memoryID uuid.UUID,
) (UnderstandingDetails, error) {
	tx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		return UnderstandingDetails{}, fmt.Errorf("begin Understanding details read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := scanMemory(tx.QueryRowContext(
		ctx,
		selectMemorySQL+` WHERE id = ?`,
		memoryID.String(),
	)); err != nil {
		return UnderstandingDetails{}, err
	}

	details := UnderstandingDetails{
		Status:    understandingStatusQueued,
		Attempt:   nil,
		ActiveRun: nil,
	}
	attempt, err := scanUnderstandingAttempt(tx.QueryRowContext(ctx, `
		SELECT id, memory_id, status, plugin_id, queued_at, started_at,
			completed_at, diagnostics_error, diagnostics_stdout, diagnostics_stderr,
			diagnostics_exit_code
		FROM understanding_attempts
		WHERE memory_id = ? ORDER BY queued_at DESC, id DESC LIMIT 1
	`, memoryID.String()))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		details.Attempt = nil
	case err != nil:
		return UnderstandingDetails{}, fmt.Errorf("read latest understanding attempt: %w", err)
	default:
		details.Attempt = &attempt
		details.Status = attempt.Status
	}

	run, err := scanUnderstandingRun(tx.QueryRowContext(ctx, `
		SELECT run.id, run.memory_id, run.attempt_id, run.plugin_id,
			run.plugin_version, run.source_blobref, run.created_at,
			run.completed_at, run.warnings_json, statistics.statistics_json
		FROM active_understanding_runs active
		JOIN understanding_runs run
			ON run.id = active.run_id AND run.memory_id = active.memory_id
		LEFT JOIN understanding_run_statistics statistics
			ON statistics.run_id = run.id
		WHERE active.memory_id = ?
	`, memoryID.String()))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		details.ActiveRun = nil
	case err != nil:
		return UnderstandingDetails{}, fmt.Errorf("read active Understanding Run: %w", err)
	default:
		if details.Attempt == nil {
			details.Status = understandingStatusDone
		}
		if err := v.readUnderstandingArtifacts(ctx, tx, &run); err != nil {
			return UnderstandingDetails{}, err
		}
		details.ActiveRun = &run
	}
	if err := tx.Commit(); err != nil {
		return UnderstandingDetails{}, fmt.Errorf("finish Understanding details read: %w", err)
	}
	return details, nil
}

func scanUnderstandingAttempt(row rowScanner) (UnderstandingAttempt, error) {
	var attempt UnderstandingAttempt
	var id, memoryID, queuedAt string
	var startedAt, completedAt sql.NullString
	var diagnosticError, stdout, stderr sql.NullString
	var exitCode sql.NullInt64
	if err := row.Scan(
		&id,
		&memoryID,
		&attempt.Status,
		&attempt.PluginID,
		&queuedAt,
		&startedAt,
		&completedAt,
		&diagnosticError,
		&stdout,
		&stderr,
		&exitCode,
	); err != nil {
		return UnderstandingAttempt{}, err
	}
	var err error
	if attempt.ID, err = uuid.Parse(id); err != nil {
		return UnderstandingAttempt{}, fmt.Errorf("read understanding attempt ID: %w", err)
	}
	if attempt.MemoryID, err = uuid.Parse(memoryID); err != nil {
		return UnderstandingAttempt{}, fmt.Errorf("read understanding Memory ID: %w", err)
	}
	if attempt.QueuedAt, err = time.Parse(time.RFC3339Nano, queuedAt); err != nil {
		return UnderstandingAttempt{}, fmt.Errorf("read understanding queue time: %w", err)
	}
	if attempt.StartedAt, err = parseNullableTime(startedAt); err != nil {
		return UnderstandingAttempt{}, err
	}
	if attempt.CompletedAt, err = parseNullableTime(completedAt); err != nil {
		return UnderstandingAttempt{}, err
	}
	if attempt.Status == understandingStatusFailed {
		diagnostics := UnderstandingDiagnostics{
			Error:    diagnosticError.String,
			Stdout:   stdout.String,
			Stderr:   stderr.String,
			ExitCode: nil,
		}
		if exitCode.Valid {
			code := int(exitCode.Int64)
			diagnostics.ExitCode = &code
		}
		attempt.Diagnostics = &diagnostics
	}
	return attempt, nil
}

func scanUnderstandingRun(row rowScanner) (UnderstandingRun, error) {
	var run UnderstandingRun
	var id, memoryID, attemptID, sourceBlobref string
	var createdAt, completedAt, warningsJSON string
	var statisticsJSON sql.NullString
	if err := row.Scan(
		&id,
		&memoryID,
		&attemptID,
		&run.PluginID,
		&run.PluginVersion,
		&sourceBlobref,
		&createdAt,
		&completedAt,
		&warningsJSON,
		&statisticsJSON,
	); err != nil {
		return UnderstandingRun{}, err
	}
	var err error
	if run.ID, err = uuid.Parse(id); err != nil {
		return UnderstandingRun{}, fmt.Errorf("read Understanding Run ID: %w", err)
	}
	if run.MemoryID, err = uuid.Parse(memoryID); err != nil {
		return UnderstandingRun{}, fmt.Errorf("read Understanding Run Memory ID: %w", err)
	}
	if run.AttemptID, err = uuid.Parse(attemptID); err != nil {
		return UnderstandingRun{}, fmt.Errorf("read Understanding Run attempt ID: %w", err)
	}
	if run.SourceBlobref, err = ParseBlobref(sourceBlobref); err != nil {
		return UnderstandingRun{}, fmt.Errorf("read Understanding Run source Blobref: %w", err)
	}
	if run.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return UnderstandingRun{}, fmt.Errorf("read Understanding Run creation time: %w", err)
	}
	if run.CompletedAt, err = time.Parse(time.RFC3339Nano, completedAt); err != nil {
		return UnderstandingRun{}, fmt.Errorf("read Understanding Run completion time: %w", err)
	}
	if err := json.Unmarshal([]byte(warningsJSON), &run.Warnings); err != nil {
		return UnderstandingRun{}, fmt.Errorf("read Understanding Run warnings: %w", err)
	}
	if statisticsJSON.Valid {
		var reporting understandingRunReporting
		if err := json.Unmarshal([]byte(statisticsJSON.String), &reporting); err != nil {
			return UnderstandingRun{}, fmt.Errorf("read Understanding Run reporting: %w", err)
		}
		run.Statistics = reporting.Statistics
		run.CostEstimate = reporting.CostEstimate
	}
	if run.Warnings == nil {
		run.Warnings = []string{}
	}
	run.Artifacts = []DerivedContent{}
	return run, nil
}

func (v *Vault) readUnderstandingArtifacts(
	ctx context.Context,
	tx *sql.Tx,
	run *UnderstandingRun,
) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, blob_hash, content_type, byte_size, provenance_json, scope_json
		FROM understanding_artifacts
		WHERE run_id = ? AND memory_id = ? ORDER BY ordinal
	`, run.ID.String(), run.MemoryID.String())
	if err != nil {
		return fmt.Errorf("read Understanding Run artifacts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var artifact DerivedContent
		var id, blobHash string
		var provenanceJSON, scopeJSON sql.NullString
		if err := rows.Scan(
			&id,
			&blobHash,
			&artifact.Blob.MediaType,
			&artifact.Blob.ByteSize,
			&provenanceJSON,
			&scopeJSON,
		); err != nil {
			return fmt.Errorf("read derived artifact: %w", err)
		}
		if artifact.ID, err = uuid.Parse(id); err != nil {
			return fmt.Errorf("read derived artifact ID: %w", err)
		}
		if artifact.Blob.Ref, err = ParseBlobref(blobHash); err != nil {
			return fmt.Errorf("read derived artifact Blobref: %w", err)
		}
		if provenanceJSON.Valid {
			artifact.Provenance = json.RawMessage(provenanceJSON.String)
		}
		if scopeJSON.Valid {
			artifact.Scope = json.RawMessage(scopeJSON.String)
		}
		artifact.Content, err = readVerifiedBlob(
			v.derivedBlobPath(artifact.Blob.Ref),
			artifact.Blob.Ref,
			artifact.Blob.ByteSize,
		)
		if err != nil {
			return err
		}
		run.Artifacts = append(run.Artifacts, artifact)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read Understanding Run artifacts: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close Understanding Run artifact rows: %w", err)
	}
	return nil
}

func readVerifiedBlob(path string, ref Blobref, expectedSize int64) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%w: read %s: %v", ErrBlobUnavailable, ref, err)
	}
	actualRef := NewSHA256Blobref(sha256.Sum256(content))
	actualSize := int64(len(content))
	if actualSize != expectedSize || actualRef != ref {
		return "", fmt.Errorf(
			"%w: %s has size %d and digest %s; expected size %d and digest %s",
			ErrBlobUnavailable,
			ref,
			actualSize,
			actualRef,
			expectedSize,
			ref,
		)
	}
	return string(content), nil
}
