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

	"github.com/anton-povarov/memoryd/server/internal/config"
	"github.com/anton-povarov/memoryd/server/internal/logging"
	"github.com/anton-povarov/memoryd/server/internal/understanding"
	"github.com/anton-povarov/memoryd/server/internal/vault"
	"github.com/google/uuid"
)

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
}

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
		closeOnce:   sync.Once{},
		workers:     sync.WaitGroup{},
	}
	if err := memoryVault.RecoverUnderstanding(ctx); err != nil {
		cancel()
		return nil, fmt.Errorf("recover understanding attempts: %w", err)
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
		wake := make(chan struct{}, 1)
		worker.wakeups[index] = wake
		worker.workers.Add(1)
		go worker.run(wake)
	}
	worker.Wake()
	return worker, nil
}

func (worker *understandingWorker) Wake() {
	for _, wake := range worker.wakeups {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

func (worker *understandingWorker) Close() {
	worker.closeOnce.Do(func() {
		worker.cancel()
		worker.workers.Wait()
		worker.logger.InfoContext(worker.ctx, "Document Understanding stopped")
	})
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
		memory, attempt, err := worker.memoryVault.ClaimUnderstanding(worker.ctx)
		if errors.Is(err, vault.ErrNoUnderstandingWork) {
			return
		}
		if err != nil {
			if worker.ctx.Err() == nil {
				worker.logger.ErrorContext(
					worker.ctx,
					"claim understanding attempt failed",
					"error",
					err,
				)
			}
			return
		}
		worker.execute(memory, attempt)
	}
}

func (worker *understandingWorker) execute(
	memory vault.Memory,
	attempt vault.UnderstandingAttempt,
) {
	runID := uuid.New()
	pluginID, plugin, matched := worker.pluginFor(memory.Blob.MediaType)
	worker.logger.InfoContext(worker.ctx, "Understanding attempt started",
		"memory_id", memory.ID.String(),
		"attempt_id", attempt.ID.String(),
		"run_id", runID.String(),
		"media_type", memory.Blob.MediaType,
		"plugin_id", pluginID,
	)

	if !matched {
		worker.finish(
			attempt,
			runID,
			"",
			nil,
			[]string{fmt.Sprintf(
				"unsupported Media Type %q: no matching understanding plugin configured",
				memory.Blob.MediaType,
			)},
			vault.UnderstandingDiagnostics{
				Error:    "",
				Stdout:   "",
				Stderr:   "",
				ExitCode: nil,
			},
			nil,
			nil,
		)
		return
	}

	if err := worker.memoryVault.SetUnderstandingPlugin(
		worker.ctx,
		attempt.ID,
		pluginID,
	); err != nil {
		worker.fail(attempt, vault.UnderstandingDiagnostics{
			Error:    fmt.Sprintf("set understanding plugin: %v", err),
			Stdout:   "",
			Stderr:   "",
			ExitCode: nil,
		}, false)
		return
	}
	attempt.PluginID = pluginID

	memory, contentReader, err := worker.memoryVault.OpenContent(worker.ctx, memory.ID)
	if err != nil {
		worker.fail(attempt, vault.UnderstandingDiagnostics{
			Error:    fmt.Sprintf("open source Blob: %v", err),
			Stdout:   "",
			Stderr:   "",
			ExitCode: nil,
		}, false)
		return
	}
	content, readErr := io.ReadAll(contentReader)
	closeErr := contentReader.Close()
	if readErr != nil {
		worker.fail(attempt, vault.UnderstandingDiagnostics{
			Error:    fmt.Sprintf("read source Blob: %v", readErr),
			Stdout:   "",
			Stderr:   "",
			ExitCode: nil,
		}, false)
		return
	}
	if closeErr != nil {
		worker.fail(attempt, vault.UnderstandingDiagnostics{
			Error:    fmt.Sprintf("close source Blob: %v", closeErr),
			Stdout:   "",
			Stderr:   "",
			ExitCode: nil,
		}, false)
		return
	}
	if worker.ctx.Err() != nil {
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
		models...,
	)
	if err != nil {
		worker.fail(attempt, vault.UnderstandingDiagnostics{
			Error:    fmt.Sprintf("build plugin request: %v", err),
			Stdout:   "",
			Stderr:   "",
			ExitCode: nil,
		}, false)
		return
	}

	worker.logger.InfoContext(worker.ctx, "Understanding Plugin started",
		"memory_id", memory.ID.String(),
		"attempt_id", attempt.ID.String(),
		"plugin_id", pluginID,
		"executable", plugin.Command[0],
	)
	progress := &understandingProgress{
		ctx: worker.ctx,
		logger: worker.logger.With(
			"memory_id", memory.ID.String(),
			"attempt_id", attempt.ID.String(),
			"plugin_id", pluginID,
		),
		pending: nil,
		discard: false,
	}
	execution := understanding.Run(worker.ctx, plugin.Command, requestBytes, progress)
	if plugin.LogDir != "" {
		worker.saveUnderstandingLogs(plugin.LogDir, runID, execution)
	}
	if worker.ctx.Err() != nil {
		worker.logger.InfoContext(
			worker.ctx,
			"Understanding attempt interrupted; retry on next server start",
			"memory_id",
			memory.ID.String(),
			"attempt_id",
			attempt.ID.String(),
			"plugin_id",
			pluginID,
		)
		return
	}
	diagnostics := vault.UnderstandingDiagnostics{
		Error:    execution.ProcessError,
		Stdout:   string(execution.Stdout),
		Stderr:   string(execution.Stderr),
		ExitCode: execution.ExitCode,
	}
	worker.logger.InfoContext(worker.ctx, "Understanding Plugin exited",
		"memory_id", memory.ID.String(),
		"attempt_id", attempt.ID.String(),
		"plugin_id", pluginID,
		"duration", execution.Duration,
		"exit_code", execution.ExitCode,
	)

	if execution.ProcessError != "" || execution.ExitCode == nil || *execution.ExitCode != 0 {
		if diagnostics.Error == "" {
			diagnostics.Error = "plugin process did not exit successfully"
		}
		worker.fail(attempt, diagnostics, true)
		return
	}

	result, err := understanding.ValidateResult(execution.Stdout)
	if err != nil {
		diagnostics.Error = "invalid plugin result: " + err.Error()
		worker.fail(attempt, diagnostics, true)
		return
	}
	if result.Statistics != nil || result.CostEstimate != nil {
		progress.logger.InfoContext(worker.ctx, "Understanding Plugin reporting",
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
		attempt,
		runID,
		result.PluginVersion,
		artifacts,
		result.Warnings,
		diagnostics,
		result.Statistics,
		result.CostEstimate,
	)
}

func (worker *understandingWorker) saveUnderstandingLogs(
	logDir string,
	runID uuid.UUID,
	execution understanding.Execution,
) {
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		worker.logger.ErrorContext(worker.ctx, "create understanding log directory failed",
			"run_id", runID.String(), "log_dir", logDir, "error", err,
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
			worker.logger.ErrorContext(worker.ctx, "write understanding log failed",
				"run_id", runID.String(), "path", path, "error", err,
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
	return "", config.UnderstandingPluginConfig{
		Command:    nil,
		MediaTypes: nil,
		LogDir:     "",
	}, false
}

func (worker *understandingWorker) finish(
	attempt vault.UnderstandingAttempt,
	runID uuid.UUID,
	pluginVersion string,
	artifacts []vault.DerivedContent,
	warnings []string,
	diagnostics vault.UnderstandingDiagnostics,
	statistics *vault.Statistics,
	costEstimate *vault.CostEstimate,
) {
	if worker.ctx.Err() != nil {
		return
	}
	err := worker.memoryVault.FinishUnderstanding(
		worker.ctx,
		attempt,
		runID,
		pluginVersion,
		artifacts,
		warnings,
		statistics,
		costEstimate,
	)
	if worker.ctx.Err() != nil {
		return
	}
	if err == nil {
		level := slog.LevelInfo

		if len(warnings) > 0 {
			level = slog.LevelWarn
		}
		worker.logger.Log(worker.ctx, level, "Understanding attempt completed",
			"memory_id", attempt.MemoryID.String(),
			"attempt_id", attempt.ID.String(),
			"plugin_id", attempt.PluginID,
			"plugin_version", pluginVersion,
			"artifact_count", len(artifacts),
			"warnings", warnings,
		)
		return
	}
	worker.logger.ErrorContext(
		worker.ctx,
		"finish understanding attempt failed",
		"attempt_id",
		attempt.ID.String(),
		"error",
		err,
	)
	if errors.Is(err, vault.ErrMemoryNotFound) {
		return
	}
	diagnostics.Error = fmt.Sprintf("finish understanding: %v", err)
	worker.fail(attempt, diagnostics, false)
}

func (worker *understandingWorker) fail(
	attempt vault.UnderstandingAttempt,
	diagnostics vault.UnderstandingDiagnostics,
	pluginFailed bool,
) {
	if worker.ctx.Err() != nil {
		return
	}
	err := worker.memoryVault.FailUnderstanding(worker.ctx, attempt.ID, diagnostics)

	if worker.ctx.Err() != nil {
		return
	}
	if err != nil {
		worker.logger.ErrorContext(worker.ctx, "fail understanding attempt persistence failed",
			"memory_id", attempt.MemoryID.String(),
			"attempt_id", attempt.ID.String(),
			"error", err,
		)
		return
	}
	var stderr string
	if pluginFailed {
		stderr = diagnostics.Stderr
	}
	worker.logger.ErrorContext(worker.ctx, "Understanding attempt failed",
		"memory_id", attempt.MemoryID.String(),
		"attempt_id", attempt.ID.String(),
		"plugin_id", attempt.PluginID,
		"error", diagnostics.Error,
		"exit_code", diagnostics.ExitCode,
		"stderr", stderr,
	)
}
