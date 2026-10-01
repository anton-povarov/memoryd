package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/anton-povarov/memoryd/server/internal/config"
	"github.com/anton-povarov/memoryd/server/internal/logging"
	"github.com/anton-povarov/memoryd/server/internal/understanding"
	"github.com/anton-povarov/memoryd/server/internal/vault"
	"github.com/google/uuid"
)

type understandingJob struct {
	memory   vault.Memory
	attempt  vault.UnderstandingAttempt
	userNote string
	ctx      context.Context
	logger   *logging.Logger
}

type understandingWorker struct {
	memoryVault *vault.Vault
	config      config.UnderstandingConfig
	model       *understanding.RequestModel
	logger      *logging.Logger
	ctx         context.Context
	cancel      context.CancelFunc
	wakeups     []chan struct{}
	closeOnce   sync.Once
	workers     sync.WaitGroup

	mu        sync.Mutex
	queue     []*understandingJob
	latest    map[uuid.UUID]*understandingJob
	byAttempt map[uuid.UUID]*understandingJob
	closed    bool
}

var errUnderstandingUnavailable = errors.New("understanding worker unavailable")

func newUnderstandingWorker(
	memoryVault *vault.Vault,
	cfg config.UnderstandingConfig,
	model *understanding.RequestModel,
	logger *logging.Logger,
) (*understandingWorker, error) {
	if cfg.MaxConcurrent < 1 {
		return nil, errors.New("understanding max_concurrent must be greater than zero")
	}
	if logger == nil {
		panic("newUnderstandingWorker: must provide a logger")
	}

	ctx, cancel := context.WithCancel(context.Background())
	worker := &understandingWorker{
		memoryVault: memoryVault,
		config:      cfg,
		model:       model,
		logger:      logger.With(logging.System("understanding")),
		ctx:         ctx,
		cancel:      cancel,
		wakeups:     make([]chan struct{}, cfg.MaxConcurrent),
		latest:      make(map[uuid.UUID]*understandingJob),
		byAttempt:   make(map[uuid.UUID]*understandingJob),
	}
	worker.logger.InfoContext(ctx, "Document Understanding started",
		"max_concurrent", cfg.MaxConcurrent,
		"plugin_count", len(cfg.Plugins),
		"model_configured", model != nil,
	)
	if len(cfg.Plugins) == 0 {
		worker.logger.WarnContext(
			ctx,
			"No Understanding Plugins configured; imports will complete with unsupported-format warnings",
		)
	}
	for index := range worker.wakeups {
		worker.wakeups[index] = make(chan struct{}, 1)
		worker.workers.Add(1)
		go worker.run(worker.wakeups[index])
	}
	return worker, nil
}

func (worker *understandingWorker) Enqueue(
	ctx context.Context,
	memoryID uuid.UUID,
	replacement *string,
) (vault.UnderstandingAttempt, bool, error) {
	worker.mu.Lock()
	closed := worker.closed
	worker.mu.Unlock()
	if closed {
		return vault.UnderstandingAttempt{}, false, errUnderstandingUnavailable
	}
	memory, err := worker.memoryVault.Memory(ctx, memoryID)
	if err != nil {
		if errors.Is(err, vault.ErrMemoryNotFound) {
			worker.Forget(memoryID)
		}
		return vault.UnderstandingAttempt{}, false, err
	}
	if err := ctx.Err(); err != nil {
		return vault.UnderstandingAttempt{}, false, err
	}

	worker.mu.Lock()
	if worker.closed {
		worker.mu.Unlock()
		return vault.UnderstandingAttempt{}, false, errUnderstandingUnavailable
	}
	if current := worker.latest[memoryID]; current != nil &&
		(current.attempt.Status == vault.UnderstandingStatusQueued ||
			current.attempt.Status == vault.UnderstandingStatusRunning) {
		attempt := cloneUnderstandingAttempt(current.attempt)
		worker.mu.Unlock()
		return attempt, false, nil
	}

	// ponytail: note persistence shares the admission lock; separate it if database latency makes polling wait.
	userNote, err := worker.memoryVault.ResolveUnderstandingUserNote(ctx, memoryID, replacement)
	if err != nil {
		worker.mu.Unlock()
		if errors.Is(err, vault.ErrMemoryNotFound) {
			worker.Forget(memoryID)
		}
		return vault.UnderstandingAttempt{}, false, err
	}

	attempt := vault.UnderstandingAttempt{
		ID:       uuid.New(),
		MemoryID: memoryID,
		Status:   vault.UnderstandingStatusQueued,
		QueuedAt: time.Now().UTC(),
	}
	jobCtx := logging.ContextWithRequestID(worker.ctx, logging.RequestID(ctx))
	job := &understandingJob{
		memory:   memory,
		attempt:  attempt,
		userNote: userNote,
		ctx:      jobCtx,
		logger: logging.ForOperationInRequest(
			worker.logger, "ExecuteUnderstanding", jobCtx,
		).With("memory_id", memoryID.String(), "attempt_id", attempt.ID.String()),
	}
	if previous := worker.latest[memoryID]; previous != nil {
		delete(worker.byAttempt, previous.attempt.ID)
	}
	worker.byAttempt[attempt.ID] = job
	worker.latest[memoryID] = job
	worker.queue = append(worker.queue, job)
	snapshot := cloneUnderstandingAttempt(attempt)
	worker.mu.Unlock()

	worker.wake()
	return snapshot, true, nil
}

func (worker *understandingWorker) Latest(
	memoryID uuid.UUID,
) (vault.UnderstandingAttempt, bool) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	job := worker.latest[memoryID]
	if job == nil {
		return vault.UnderstandingAttempt{}, false
	}
	return cloneUnderstandingAttempt(job.attempt), true
}

func (worker *understandingWorker) Snapshot(
	attemptID uuid.UUID,
) (vault.UnderstandingAttempt, bool) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	job := worker.byAttempt[attemptID]
	if job == nil {
		return vault.UnderstandingAttempt{}, false
	}
	return cloneUnderstandingAttempt(job.attempt), true
}

func (worker *understandingWorker) Forget(memoryID uuid.UUID) {
	worker.mu.Lock()
	if job := worker.latest[memoryID]; job != nil {
		delete(worker.byAttempt, job.attempt.ID)
	}
	delete(worker.latest, memoryID)
	kept := worker.queue[:0]
	for _, job := range worker.queue {
		if job.memory.ID != memoryID {
			kept = append(kept, job)
		}
	}
	clear(worker.queue[len(kept):])
	worker.queue = kept
	worker.mu.Unlock()
}

func (worker *understandingWorker) Close() {
	worker.closeOnce.Do(func() {
		worker.mu.Lock()
		worker.closed = true
		worker.queue = nil
		clear(worker.latest)
		clear(worker.byAttempt)
		worker.mu.Unlock()
		worker.cancel()
		worker.workers.Wait()
		worker.logger.InfoContext(worker.ctx, "Document Understanding stopped")
	})
}

func (worker *understandingWorker) wake() {
	for _, wake := range worker.wakeups {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

func (worker *understandingWorker) run(wake <-chan struct{}) {
	defer worker.workers.Done()
	for {
		select {
		case <-worker.ctx.Done():
			return
		case <-wake:
			worker.drainQueue()
		}
	}
}

func (worker *understandingWorker) drainQueue() {
	for worker.ctx.Err() == nil {
		job, attempt, ok := worker.claim()
		if !ok {
			return
		}
		worker.execute(job, attempt)
	}
}

func (worker *understandingWorker) claim() (*understandingJob, vault.UnderstandingAttempt, bool) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	for !worker.closed && len(worker.queue) > 0 {
		job := worker.queue[0]
		worker.queue[0] = nil
		worker.queue = worker.queue[1:]
		if len(worker.queue) == 0 {
			worker.queue = nil
		}
		if worker.latest[job.memory.ID] != job {
			continue
		}
		startedAt := time.Now().UTC()
		job.attempt.Status = vault.UnderstandingStatusRunning
		job.attempt.StartedAt = &startedAt
		return job, cloneUnderstandingAttempt(job.attempt), true
	}
	return nil, vault.UnderstandingAttempt{}, false
}

func cloneUnderstandingAttempt(attempt vault.UnderstandingAttempt) vault.UnderstandingAttempt {
	if attempt.StartedAt != nil {
		startedAt := *attempt.StartedAt
		attempt.StartedAt = &startedAt
	}
	if attempt.CompletedAt != nil {
		completedAt := *attempt.CompletedAt
		attempt.CompletedAt = &completedAt
	}
	if attempt.Diagnostics != nil {
		diagnostics := *attempt.Diagnostics
		if diagnostics.ExitCode != nil {
			exitCode := *diagnostics.ExitCode
			diagnostics.ExitCode = &exitCode
		}
		attempt.Diagnostics = &diagnostics
	}
	return attempt
}

func (worker *understandingWorker) setPlugin(
	job *understandingJob,
	attempt vault.UnderstandingAttempt,
	pluginID string,
) vault.UnderstandingAttempt {
	worker.mu.Lock()
	job.attempt.PluginID = pluginID
	attempt.PluginID = pluginID
	worker.mu.Unlock()
	return attempt
}

func (worker *understandingWorker) publish(
	job *understandingJob,
	attempt vault.UnderstandingAttempt,
) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.latest[job.memory.ID] == job {
		job.attempt = cloneUnderstandingAttempt(attempt)
	}
}

func (worker *understandingWorker) forgetJob(job *understandingJob) {
	worker.mu.Lock()
	if worker.latest[job.memory.ID] == job {
		delete(worker.latest, job.memory.ID)
	}
	worker.mu.Unlock()
}

func (worker *understandingWorker) execute(
	job *understandingJob,
	attempt vault.UnderstandingAttempt,
) {
	memory := job.memory
	runID := uuid.New()
	runLogger := job.logger.With("run_id", runID.String())
	pluginID, plugin, matched := worker.pluginFor(memory.Blob.MediaType)
	attempt = worker.setPlugin(job, attempt, pluginID)
	logger := runLogger.With("plugin_id", pluginID)
	logger.InfoContext(job.ctx, "Understanding attempt started",
		"media_type", memory.Blob.MediaType,
	)

	if !matched {
		worker.finish(job, attempt, runID, logger, "", nil, []string{fmt.Sprintf(
			"unsupported Media Type %q: no matching understanding plugin configured",
			memory.Blob.MediaType,
		)}, vault.UnderstandingDiagnostics{}, nil, nil)
		return
	}

	memory, contentReader, err := worker.memoryVault.OpenContent(job.ctx, memory.ID)
	if err != nil {
		worker.fail(job, attempt, logger, vault.UnderstandingDiagnostics{
			Error: fmt.Sprintf("open source Blob: %v", err),
		}, false)
		return
	}
	content, readErr := io.ReadAll(contentReader)
	closeErr := contentReader.Close()
	if readErr != nil {
		worker.fail(job, attempt, logger, vault.UnderstandingDiagnostics{
			Error: fmt.Sprintf("read source Blob: %v", readErr),
		}, false)
		return
	}
	if closeErr != nil {
		worker.fail(job, attempt, logger, vault.UnderstandingDiagnostics{
			Error: fmt.Sprintf("close source Blob: %v", closeErr),
		}, false)
		return
	}
	if job.ctx.Err() != nil {
		return
	}

	var models []understanding.RequestModel
	if worker.model != nil {
		models = []understanding.RequestModel{*worker.model}
	}
	_, requestBytes, err := understanding.BuildRequest(
		content,
		memory.Blob.MediaType,
		memory.ImportContext,
		job.userNote,
		models...,
	)
	if err != nil {
		worker.fail(job, attempt, logger, vault.UnderstandingDiagnostics{
			Error: fmt.Sprintf("build plugin request: %v", err),
		}, false)
		return
	}

	logger.InfoContext(job.ctx, "Understanding Plugin started",
		"executable", plugin.Command[0],
	)
	progress := &understandingProgress{
		ctx:    job.ctx,
		logger: logger,
	}
	execution := understanding.Run(job.ctx, plugin.Command, requestBytes, progress)
	if plugin.LogDir != "" {
		worker.saveUnderstandingLogs(job.ctx, logger, plugin.LogDir, runID, execution)
	}
	if job.ctx.Err() != nil {
		logger.InfoContext(job.ctx, "Understanding attempt interrupted")
		return
	}
	diagnostics := vault.UnderstandingDiagnostics{
		Error:    execution.ProcessError,
		Stdout:   string(execution.Stdout),
		Stderr:   string(execution.Stderr),
		ExitCode: execution.ExitCode,
	}
	logger.InfoContext(job.ctx, "Understanding Plugin exited",
		"duration", execution.Duration,
		"exit_code", execution.ExitCode,
	)

	if execution.ProcessError != "" || execution.ExitCode == nil || *execution.ExitCode != 0 {
		if diagnostics.Error == "" {
			diagnostics.Error = "plugin process did not exit successfully"
		}
		worker.fail(job, attempt, logger, diagnostics, true)
		return
	}

	result, err := understanding.ValidateResult(execution.Stdout)
	if err != nil {
		diagnostics.Error = "invalid plugin result: " + err.Error()
		worker.fail(job, attempt, logger, diagnostics, true)
		return
	}
	if result.Statistics != nil || result.CostEstimate != nil {
		logger.InfoContext(job.ctx, "Understanding Plugin reporting",
			"statistics", result.Statistics,
			"cost_estimate", result.CostEstimate,
		)
	}

	artifacts := make([]vault.DerivedContent, 0, len(result.Artifacts))
	for _, artifact := range result.Artifacts {
		artifacts = append(artifacts, vault.DerivedContent{
			ID: uuid.Nil,
			Blob: vault.BlobInfo{
				Ref:       vault.Blobref{},
				MediaType: artifact.ContentType,
				ByteSize:  0,
			},
			Content:    artifact.Content,
			Provenance: artifact.Provenance,
			Scope:      artifact.Scope,
		})
	}
	worker.finish(
		job, attempt, runID, logger, result.PluginVersion, artifacts, result.Warnings,
		diagnostics, result.Statistics, result.CostEstimate,
	)
}

func (worker *understandingWorker) saveUnderstandingLogs(
	ctx context.Context,
	logger *logging.Logger,
	logDir string,
	runID uuid.UUID,
	execution understanding.Execution,
) {
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		logger.ErrorContext(ctx, "create understanding log directory failed",
			"log_dir", logDir, "error", err,
		)
		return
	}
	for _, stream := range []struct {
		name string
		data []byte
	}{
		{name: "stdout", data: execution.Stdout},
		{name: "stderr", data: execution.Stderr},
	} {
		path := filepath.Join(logDir, runID.String()+"."+stream.name+".log")
		if err := os.WriteFile(path, stream.data, 0o600); err != nil {
			logger.ErrorContext(ctx, "write understanding log failed",
				"path", path, "error", err,
			)
		}
	}
}

func (worker *understandingWorker) pluginFor(
	mediaType string,
) (string, config.UnderstandingPluginConfig, bool) {
	for pluginID, plugin := range worker.config.Plugins {
		for _, supportedMediaType := range plugin.MediaTypes {
			if supportedMediaType == mediaType {
				return pluginID, plugin, true
			}
		}
	}
	return "", config.UnderstandingPluginConfig{}, false
}

func (worker *understandingWorker) finish(
	job *understandingJob,
	attempt vault.UnderstandingAttempt,
	runID uuid.UUID,
	logger *logging.Logger,
	pluginVersion string,
	artifacts []vault.DerivedContent,
	warnings []string,
	diagnostics vault.UnderstandingDiagnostics,
	statistics *vault.Statistics,
	costEstimate *vault.CostEstimate,
) {
	if job.ctx.Err() != nil {
		return
	}
	completed, err := worker.memoryVault.FinishUnderstanding(
		job.ctx,
		attempt,
		runID,
		pluginVersion,
		artifacts,
		warnings,
		statistics,
		costEstimate,
		job.userNote,
	)
	if job.ctx.Err() != nil {
		return
	}
	if err != nil {
		logger.ErrorContext(job.ctx, "finish understanding attempt failed", "error", err)
		if errors.Is(err, vault.ErrMemoryNotFound) {
			worker.forgetJob(job)
			return
		}
		diagnostics.Error = fmt.Sprintf("finish understanding: %v", err)
		worker.fail(job, attempt, logger, diagnostics, false)
		return
	}

	worker.publish(job, completed)
	level := slog.LevelInfo
	if len(warnings) > 0 {
		level = slog.LevelWarn
	}
	logger.Log(job.ctx, level, "Understanding attempt completed",
		"plugin_version", pluginVersion,
		"artifact_count", len(artifacts),
		"warnings", warnings,
	)
}

func (worker *understandingWorker) fail(
	job *understandingJob,
	attempt vault.UnderstandingAttempt,
	logger *logging.Logger,
	diagnostics vault.UnderstandingDiagnostics,
	pluginFailed bool,
) {
	if job.ctx.Err() != nil {
		return
	}
	failed, err := worker.memoryVault.FailUnderstanding(job.ctx, attempt, diagnostics)
	if job.ctx.Err() != nil {
		return
	}
	if errors.Is(err, vault.ErrMemoryNotFound) {
		worker.forgetJob(job)
		return
	}
	if err != nil {
		logger.ErrorContext(job.ctx, "fail understanding attempt persistence failed", "error", err)
		failed = attempt
		failed.Status = vault.UnderstandingStatusFailed
		completedAt := time.Now().UTC()
		failed.CompletedAt = &completedAt
		diagnosticSnapshot := diagnostics
		failed.Diagnostics = &diagnosticSnapshot
	} else {
		failed = cloneUnderstandingAttempt(failed)
	}
	worker.publish(job, failed)

	var stderr string
	if pluginFailed {
		stderr = diagnostics.Stderr
	}
	logger.ErrorContext(job.ctx, "Understanding attempt failed",
		"error", diagnostics.Error,
		"exit_code", diagnostics.ExitCode,
		"stderr", stderr,
		"failure_persisted", err == nil,
	)
}
