package vault

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anton-povarov/memoryd/server/internal/logging"
	"github.com/google/uuid"
)

func TestCustomDatabaseDirectorySupportsDurableImport(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	logger := logging.New(slog.LevelError, io.Discard)
	dbPath := filepath.Join(root, "database", "nested", "memoryd.sqlite")
	blobDir := filepath.Join(root, "blobs")
	uploadDir := filepath.Join(root, "uploads")
	v, err := Open(ctx, logger, dbPath, blobDir, uploadDir)

	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	memory, err := v.Put(ctx, Import{
		Content:       strings.NewReader("custom database directory fixture"),
		MediaTypeHint: "text/plain",
		Context: ImportContext{
			OriginalFilename:   "note.txt",
			RelativePath:       "",
			FullPath:           "",
			FilesystemCreated:  nil,
			FilesystemModified: nil,
		},
	})

	if err != nil {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, logger, dbPath, blobDir, uploadDir)

	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	_, content, err := reopened.OpenContent(ctx, memory.ID)

	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = content.Close() }()
	bytes, err := io.ReadAll(content)

	if err != nil || string(bytes) != "custom database directory fixture" {
		t.Fatalf("durable content = %q, error %v", bytes, err)
	}
}

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
			got, err := ResolveMediaType(path, test.declared)
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

func openVault(ctx context.Context, root string) (*Vault, error) {
	logger := logging.New(slog.LevelInfo, io.Discard)
	databasePath := filepath.Join(root, "memoryd.sqlite")
	blobDir := filepath.Join(root, "blobs")
	uploadDir := filepath.Join(root, "uploads")
	return Open(ctx, logger, databasePath, blobDir, uploadDir)
}

func TestVaultPutOpenContentPersistsAndRejectsDuplicate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir() // reused a couple times
	wantBytes := []byte("%PDF-1.7\nbyte-exact memory\n%%EOF\n")
	modifiedAt := time.Date(2026, 9, 15, 10, 11, 12, 0, time.FixedZone("GST", 4*60*60))

	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	created, err := v.Put(ctx, Import{
		Content:       bytes.NewReader(wantBytes),
		MediaTypeHint: "",
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
	if created.ImportContext.FilesystemModified == nil ||
		created.ImportContext.FilesystemModified.Location() != time.UTC {
		t.Fatalf("created Import Context time was not normalized: %#v", created.ImportContext)
	}
	if created.Blob.MediaType != "application/pdf" {
		t.Fatalf("created Memory = %#v", created)
	}
	wantBlobref := NewSHA256Blobref(sha256.Sum256(wantBytes))
	if created.Blob.Ref != wantBlobref {
		t.Fatalf("Blobref = %s, want %s", created.Blob.Ref, wantBlobref)
	}

	var storedRef string
	if err := v.db.QueryRowContext(
		ctx,
		`SELECT blob_hash FROM memories WHERE id = ?`,
		created.ID.String(),
	).Scan(&storedRef); err != nil {
		t.Fatal(err)
	}
	if storedRef != created.Blob.Ref.String() {
		t.Fatalf("stored Blobref = %q, want %q", storedRef, created.Blob.Ref)
	}
	if _, err := v.db.ExecContext(
		ctx,
		`UPDATE memories SET blob_hash = ? WHERE id = ?`,
		created.Blob.Ref.digestHex(),
		created.ID.String(),
	); err == nil {
		t.Fatal("database accepted an unprefixed digest")
	}
	blobPath := v.blobPath(wantBlobref)
	storedBytes, err := os.ReadFile(blobPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedBytes, wantBytes) {
		t.Fatalf("stored content = %q, want %q", storedBytes, wantBytes)
	}
	stagedEntries, err := os.ReadDir(v.uploadDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(stagedEntries) != 0 {
		t.Fatalf("upload directory contains %d staged files", len(stagedEntries))
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}

	v, err = openVault(ctx, root)
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
	if opened.ID != created.ID || opened.Blob.Ref != created.Blob.Ref ||
		opened.ImportContext.OriginalFilename != "notes.pdf" {
		t.Fatalf("reopened Memory = %#v, created = %#v", opened, created)
	}
	if opened.ImportContext.FullPath != "/original/notes.pdf" ||
		opened.ImportContext.FilesystemCreated != nil ||
		opened.ImportContext.FilesystemModified == nil ||
		!opened.ImportContext.FilesystemModified.Equal(modifiedAt) {
		t.Fatalf("reopened Import Context = %#v", opened.ImportContext)
	}
	_, err = v.Put(ctx, Import{
		Content:       bytes.NewReader(wantBytes),
		MediaTypeHint: "",
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

func TestVaultDeleteRemovesMemoryRunsAndBlob(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })

	wantBytes := []byte("%PDF-1.7\ndelete me\n%%EOF\n")
	memory, err := v.Put(ctx, Import{
		Content:       bytes.NewReader(wantBytes),
		MediaTypeHint: "",
		Context: ImportContext{
			OriginalFilename:   "remove.pdf",
			RelativePath:       "",
			FullPath:           "",
			FilesystemCreated:  nil,
			FilesystemModified: nil,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = v.db.ExecContext(ctx, `
		INSERT INTO understanding_attempts (id, memory_id, status, queued_at)
		VALUES (?, ?, ?, ?)
	`, "attempt", memory.ID.String(), understandingStatusDone, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = v.db.ExecContext(
		ctx,
		`INSERT INTO understanding_runs (
			id, memory_id, attempt_id, plugin_id, plugin_version, source_blobref,
			created_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"run",
		memory.ID.String(),
		"attempt",
		"plugin",
		"version",
		memory.Blob.Ref.String(),
		now,
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = v.db.ExecContext(
		ctx,
		`INSERT INTO active_understanding_runs (memory_id, run_id) VALUES (?, ?)`,
		memory.ID.String(),
		"run",
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = v.db.ExecContext(ctx, `
		INSERT INTO understanding_artifacts (
			id, run_id, memory_id, ordinal, blob_hash, content_type,
			byte_size, provenance_json, scope_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "artifact", "run", memory.ID.String(), 0, memory.Blob.Ref.String(),
		"text/markdown", int64(1), "{}", "null")
	if err != nil {
		t.Fatal(err)
	}
	_, err = v.db.ExecContext(ctx, `
		INSERT INTO understanding_run_statistics (run_id, statistics_json)
		VALUES (?, ?)
	`, "run", `{"usage":{"input_tokens":1}}`)
	if err != nil {
		t.Fatal(err)
	}
	blobPath := v.blobPath(memory.Blob.Ref)
	if _, err := os.Stat(blobPath); err != nil {
		t.Fatal(err)
	}

	if err := v.Delete(ctx, memory.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Memory(ctx, memory.ID); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("Memory lookup after delete = %v, want ErrMemoryNotFound", err)
	}
	if _, err := os.Stat(blobPath); err == nil {
		t.Fatal("Blob file remains after Memory deletion")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspect removed Blob: %v", err)
	}
	var runCount, attemptCount, artifactCount, activeRunCount int
	if err := v.db.QueryRowContext(
		ctx,
		`SELECT count(*) FROM understanding_runs WHERE memory_id = ?`,
		memory.ID.String(),
	).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if err := v.db.QueryRowContext(
		ctx,
		`SELECT count(*) FROM understanding_attempts WHERE memory_id = ?`,
		memory.ID.String(),
	).Scan(&attemptCount); err != nil {
		t.Fatal(err)
	}
	if err := v.db.QueryRowContext(
		ctx,
		`SELECT count(*) FROM understanding_artifacts WHERE memory_id = ?`,
		memory.ID.String(),
	).Scan(&artifactCount); err != nil {
		t.Fatal(err)
	}
	if err := v.db.QueryRowContext(
		ctx,
		`SELECT count(*) FROM active_understanding_runs WHERE memory_id = ?`,
		memory.ID.String(),
	).Scan(&activeRunCount); err != nil {
		t.Fatal(err)
	}
	var statisticsCount int
	if err := v.db.QueryRowContext(
		ctx,
		`SELECT count(*) FROM understanding_run_statistics WHERE run_id = ?`,
		"run",
	).Scan(&statisticsCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 0 || attemptCount != 0 || artifactCount != 0 ||
		activeRunCount != 0 || statisticsCount != 0 {
		t.Fatalf(
			"understanding rows remain after Memory deletion: runs=%d attempts=%d artifacts=%d active=%d statistics=%d",
			runCount,
			attemptCount,
			artifactCount,
			activeRunCount,
			statisticsCount,
		)
	}
	if err := v.Delete(ctx, memory.ID); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("second delete = %v, want ErrMemoryNotFound", err)
	}
}

func TestUnderstandingIgnoresLegacyPendingAttemptsAndPersistsFailures(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	initial := v
	t.Cleanup(func() { _ = initial.Close() })
	memory, err := v.Put(ctx, Import{
		Content:       strings.NewReader("understanding history"),
		MediaTypeHint: "text/plain",
		Context:       ImportContext{OriginalFilename: "history.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, legacyStatus := range []string{"queued", understandingStatusRunning} {
		if _, err := v.db.ExecContext(ctx, `
			INSERT INTO understanding_attempts (id, memory_id, status, queued_at)
			VALUES (?, ?, ?, ?)
		`, uuid.NewString(), memory.ID.String(), legacyStatus,
			formatUnderstandingTime(time.Now().UTC())); err != nil {
			t.Fatal(err)
		}
	}
	details, err := v.Understanding(ctx, memory.ID)
	if err != nil {
		t.Fatal(err)
	}
	if details.Status != "not_started" || details.Attempt != nil {
		t.Fatalf("legacy pending rows surfaced as current state: %#v", details)
	}

	queuedAt := time.Now().UTC().Add(-time.Minute)
	startedAt := queuedAt.Add(time.Second)
	input := UnderstandingAttempt{
		ID: uuid.New(), MemoryID: memory.ID, Status: understandingStatusRunning,
		PluginID: "codex", QueuedAt: queuedAt, StartedAt: &startedAt,
	}
	exitCode := 23
	diagnostics := UnderstandingDiagnostics{
		Error: "plugin failed", Stdout: "partial result", Stderr: "error output",
		ExitCode: &exitCode,
	}
	failed, err := v.FailUnderstanding(ctx, input, diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != understandingStatusFailed || failed.CompletedAt == nil ||
		failed.Diagnostics == nil || failed.Diagnostics.Error != diagnostics.Error {
		t.Fatalf("failed attempt snapshot = %#v", failed)
	}
	if _, err := v.db.ExecContext(ctx, `
		INSERT INTO understanding_attempts (id, memory_id, status, queued_at)
		VALUES (?, ?, 'queued', ?)
	`, uuid.NewString(), memory.ID.String(),
		formatUnderstandingTime(time.Now().UTC().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	v, err = openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	reopened := v
	t.Cleanup(func() { _ = reopened.Close() })
	details, err = v.Understanding(ctx, memory.ID)
	if err != nil {
		t.Fatal(err)
	}
	if details.Status != understandingStatusFailed || details.Attempt == nil ||
		details.Attempt.ID != input.ID || details.Attempt.CompletedAt == nil ||
		!details.Attempt.CompletedAt.Equal(*failed.CompletedAt) ||
		details.Attempt.Diagnostics == nil ||
		details.Attempt.Diagnostics.ExitCode == nil ||
		*details.Attempt.Diagnostics.ExitCode != exitCode || details.ActiveRun != nil {
		t.Fatalf("durable failed state = %#v", details)
	}
	if err := v.Delete(ctx, memory.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := v.FinishUnderstanding(
		ctx,
		input,
		uuid.New(),
		"1.0",
		nil,
		nil,
		nil,
		nil,
	); !errors.Is(
		err,
		ErrMemoryNotFound,
	) {
		t.Fatalf("successful attempt for deleted Memory = %v, want ErrMemoryNotFound", err)
	}
	if _, err := v.FailUnderstanding(ctx, input, diagnostics); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("failed attempt for deleted Memory = %v, want ErrMemoryNotFound", err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUnderstandingRejectsFailureForMissingMemory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	v, err := openVault(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	now := time.Now().UTC()
	attempt := UnderstandingAttempt{
		ID: uuid.New(), MemoryID: uuid.New(), Status: understandingStatusRunning,
		QueuedAt: now, StartedAt: &now,
	}
	if _, err := v.FailUnderstanding(
		ctx,
		attempt,
		UnderstandingDiagnostics{},
	); !errors.Is(
		err,
		ErrMemoryNotFound,
	) {
		t.Fatalf("failed attempt for missing Memory = %v, want ErrMemoryNotFound", err)
	}
}

func TestUnderstandingReportingPersistsAcrossReopen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	initial := v
	t.Cleanup(func() { _ = initial.Close() })

	put := func(filename, content string) Memory {
		t.Helper()
		memory, err := v.Put(ctx, Import{
			Content:       strings.NewReader(content),
			MediaTypeHint: "text/plain",
			Context: ImportContext{
				OriginalFilename: filename,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return memory
	}
	finish := func(memory Memory, statistics *Statistics, costEstimate *CostEstimate) UnderstandingAttempt {
		t.Helper()
		queuedAt := time.Now().UTC().Add(-time.Minute)
		startedAt := queuedAt.Add(time.Second)
		attempt, err := v.FinishUnderstanding(ctx, UnderstandingAttempt{
			ID: uuid.New(), MemoryID: memory.ID, Status: understandingStatusRunning,
			PluginID: "codex", QueuedAt: queuedAt, StartedAt: &startedAt,
		}, uuid.New(), "1.0", []DerivedContent{{
			Content: "# Derived\n",
			Blob:    BlobInfo{MediaType: "text/markdown"},
		}}, nil, statistics, costEstimate)
		if err != nil {
			t.Fatal(err)
		}
		if attempt.Status != understandingStatusDone || attempt.CompletedAt == nil {
			t.Fatalf("committed attempt = %#v", attempt)
		}
		return attempt
	}

	withReporting := put("with-reporting.txt", "first memory")
	inputTokens, cachedInputTokens, cacheWriteInputTokens := int64(13), int64(2), int64(3)
	outputTokens, reasoningOutputTokens, totalTokens := int64(5), int64(1), int64(18)
	wantStatistics := &Statistics{Usage: &TokenUsage{
		InputTokens:           &inputTokens,
		CachedInputTokens:     &cachedInputTokens,
		CacheWriteInputTokens: &cacheWriteInputTokens,
		OutputTokens:          &outputTokens,
		ReasoningOutputTokens: &reasoningOutputTokens,
		TotalTokens:           &totalTokens,
	}}
	wantCostEstimate := &CostEstimate{
		AmountUSD:   0.0012,
		Basis:       "API-equivalent list pricing",
		PricingDate: "2026-09-30",
		PricingURL:  "https://example.invalid/pricing",
	}
	firstTerminal := finish(withReporting, wantStatistics, wantCostEstimate)
	withoutReporting := put("without-reporting.txt", "second memory")
	finish(withoutReporting, nil, nil)

	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	v, err = openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	reopened := v
	t.Cleanup(func() { _ = reopened.Close() })

	details, err := v.Understanding(ctx, withReporting.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertCommittedUnderstandingAttempt(t, details.Attempt, firstTerminal)
	if details.ActiveRun == nil ||
		!reflect.DeepEqual(details.ActiveRun.Statistics, wantStatistics) ||
		!reflect.DeepEqual(details.ActiveRun.CostEstimate, wantCostEstimate) {
		t.Fatalf("reopened Run reporting = %#v", details.ActiveRun)
	}
	details, err = v.Understanding(ctx, withoutReporting.ID)
	if err != nil {
		t.Fatal(err)
	}
	if details.ActiveRun == nil || details.ActiveRun.Statistics != nil ||
		details.ActiveRun.CostEstimate != nil || len(details.ActiveRun.Artifacts) != 1 {
		t.Fatalf("Run without reporting = %#v", details.ActiveRun)
	}

	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", filepath.Join(root, "memoryd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `DROP TABLE understanding_run_statistics`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	v, err = openVault(ctx, root)
	if err != nil {
		t.Fatalf("reopen pre-statistics schema: %v", err)
	}
	t.Cleanup(func() { _ = v.Close() })
	details, err = v.Understanding(ctx, withReporting.ID)
	if err != nil {
		t.Fatalf("read existing Run after additive schema creation: %v", err)
	}
	if details.ActiveRun == nil || details.ActiveRun.Statistics != nil ||
		details.ActiveRun.CostEstimate != nil ||
		len(
			details.ActiveRun.Artifacts,
		) != 1 || details.ActiveRun.Artifacts[0].Content != "# Derived\n" {
		t.Fatalf("existing Run after schema upgrade = %#v", details.ActiveRun)
	}
}

func assertCommittedUnderstandingAttempt(
	t *testing.T,
	got *UnderstandingAttempt,
	want UnderstandingAttempt,
) {
	t.Helper()
	if got == nil || got.ID != want.ID || got.Status != understandingStatusDone ||
		got.CompletedAt == nil || !got.CompletedAt.Equal(*want.CompletedAt) {
		t.Fatalf("reopened terminal attempt = %#v; committed snapshot %#v", got, want)
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

func TestVaultPutAcceptsExactlyOneHundredMiB(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })

	memory, err := v.Put(ctx, Import{
		Content: io.LimitReader(repeatedByteReader{'x'}, MaxBlobBytes),
		Context: ImportContext{
			OriginalFilename:   "maximum.bin",
			RelativePath:       "",
			FullPath:           "",
			FilesystemCreated:  nil,
			FilesystemModified: nil,
		},
		MediaTypeHint: "application/octet-stream",
	})
	if err != nil {
		t.Fatal(err)
	}
	if memory.Blob.ByteSize != MaxBlobBytes {
		t.Fatalf("ByteSize = %d, want %d", memory.Blob.ByteSize, MaxBlobBytes)
	}
}

type repeatedByteReader struct {
	value byte
}

func (reader repeatedByteReader) Read(destination []byte) (int, error) {
	for index := range destination {
		destination[index] = reader.value
	}
	return len(destination), nil
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
		id TEXT PRIMARY KEY, blob_hash TEXT NOT NULL UNIQUE,
		imported_at TEXT NOT NULL DEFAULT ''
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
	_, err = openVault(ctx, root)
	if err == nil {
		t.Fatal("expected invalid stored Blobref error")
	}
	if !strings.Contains(err.Error(), "invalid stored Blobref") {
		t.Fatalf("unexpected error = %v", err)
	}
}

func TestOpenRejectsLegacyMixedMemorySchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "memoryd.sqlite")
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.ExecContext(ctx, `CREATE TABLE memories (
		id TEXT PRIMARY KEY,
		blob_hash TEXT NOT NULL UNIQUE,
		original_filename TEXT NOT NULL,
		relative_path TEXT,
		full_path TEXT,
		filesystem_created_at TEXT,
		filesystem_modified_at TEXT,
		media_type TEXT NOT NULL,
		byte_size INTEGER NOT NULL,
		imported_at TEXT NOT NULL,
		understanding_state TEXT NOT NULL,
		run_id TEXT,
		understanding_completed_at TEXT
	)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = openVault(ctx, root)
	if err == nil || !strings.Contains(err.Error(), "recreate the database and reimport") {
		t.Fatalf("Open() error = %v", err)
	}
}

func TestPutRejectsCorruptExistingBlob(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })

	content := []byte("expected content")
	digest := sha256.Sum256(content)
	ref := NewSHA256Blobref(digest)
	path := v.blobPath(ref)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupted bytes!"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = v.Put(ctx, Import{
		Content: bytes.NewReader(content),
		Context: ImportContext{
			OriginalFilename:   "memory.bin",
			RelativePath:       "",
			FullPath:           "",
			FilesystemCreated:  nil,
			FilesystemModified: nil,
		},
		MediaTypeHint: "application/octet-stream",
	})
	if !errors.Is(err, ErrBlobUnavailable) {
		t.Fatalf("Put() error = %v, want ErrBlobUnavailable", err)
	}
	memories, _, listErr := v.ListMemories(ctx, DefaultListLimit, nil)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(memories) != 0 {
		t.Fatalf("committed Memories = %d, want 0", len(memories))
	}
}

func TestConcurrentIdenticalImportsPublishOneBlobAndMemory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })

	content := []byte("same immutable bytes")
	const importers = 8
	errorsFromImports := make(chan error, importers)
	var waitGroup sync.WaitGroup
	for index := 0; index < importers; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, err := v.Put(ctx, Import{
				Content: bytes.NewReader(content),
				Context: ImportContext{
					OriginalFilename:   "same.txt",
					RelativePath:       "",
					FullPath:           "",
					FilesystemCreated:  nil,
					FilesystemModified: nil,
				},
				MediaTypeHint: "text/plain",
			})
			errorsFromImports <- err
		}()
	}
	waitGroup.Wait()
	close(errorsFromImports)

	succeeded := 0
	duplicates := 0
	for err := range errorsFromImports {
		var duplicate *DuplicateError
		switch {
		case err == nil:
			succeeded++
		case errors.As(err, &duplicate):
			duplicates++
		default:
			t.Fatalf("concurrent Put() error = %v", err)
		}
	}
	if succeeded != 1 || duplicates != importers-1 {
		t.Fatalf("successful=%d duplicate=%d", succeeded, duplicates)
	}

	digest := sha256.Sum256(content)
	ref := NewSHA256Blobref(digest)
	if err := verifyBlob(v.blobPath(ref), ref, int64(len(content))); err != nil {
		t.Fatal(err)
	}
}

func TestStartupCleansStagingAndPreservesPublishedOrphan(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	uploadDir := filepath.Join(root, "uploads")
	contentDir := filepath.Join(root, "blobs", "sha256")
	orphanBytes := []byte("published orphan")
	orphanRef := NewSHA256Blobref(sha256.Sum256(orphanBytes))
	stagingVault := Vault{
		db:        nil,
		blobDir:   contentDir,
		uploadDir: uploadDir,
	}
	orphan := stagingVault.blobPath(orphanRef)
	shardDir := filepath.Dir(orphan)
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(shardDir, 0o755); err != nil {
		t.Fatal(err)
	}
	importTemporary := filepath.Join(uploadDir, ".import-abandoned")
	publishTemporary := filepath.Join(shardDir, ".publish-abandoned")
	for _, path := range []string{importTemporary, publishTemporary} {
		if err := os.WriteFile(path, []byte("bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(orphan, orphanBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	for _, path := range []string{importTemporary, publishTemporary} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("temporary path %s still exists: %v", path, err)
		}
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("published Orphan Blob was removed: %v", err)
	}
}

func TestOpenContentDiagnosesMissingAndCorruptBlob(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	content := []byte("original")
	memory, err := v.Put(ctx, Import{
		Content: bytes.NewReader(content),
		Context: ImportContext{
			OriginalFilename:   "memory.txt",
			RelativePath:       "",
			FullPath:           "",
			FilesystemCreated:  nil,
			FilesystemModified: nil,
		},
		MediaTypeHint: "text/plain",
	})
	if err != nil {
		t.Fatal(err)
	}
	path := v.blobPath(memory.Blob.Ref)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := v.OpenContent(ctx, memory.ID); !errors.Is(err, ErrBlobUnavailable) {
		t.Fatalf("missing Blob error = %v", err)
	}
	if err := os.WriteFile(path, []byte("changed!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := v.OpenContent(ctx, memory.ID); !errors.Is(err, ErrBlobUnavailable) {
		t.Fatalf("corrupt Blob error = %v", err)
	}
}

func TestInterruptedCommitLeavesPublishedOrphanForRestart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	v, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	content := []byte("published before failed commit")
	_, err = v.Put(ctx, Import{
		Content: bytes.NewReader(content),
		Context: ImportContext{
			OriginalFilename:   "orphan.txt",
			RelativePath:       "",
			FullPath:           "",
			FilesystemCreated:  nil,
			FilesystemModified: nil,
		},
		MediaTypeHint: "text/plain",
	})
	if err == nil {
		t.Fatal("Put succeeded with a closed database")
	}

	digest := sha256.Sum256(content)
	ref := NewSHA256Blobref(digest)
	orphanPath := v.blobPath(ref)
	reopened, err := openVault(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if err := verifyBlob(orphanPath, ref, int64(len(content))); err != nil {
		t.Fatalf("published Orphan Blob did not survive restart: %v", err)
	}
	memories, _, err := reopened.ListMemories(ctx, DefaultListLimit, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 0 {
		t.Fatalf("failed commit left %d Memories", len(memories))
	}
}
