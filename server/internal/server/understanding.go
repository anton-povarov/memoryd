package server

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	pluginID, plugin, matched := worker.pluginFor(memory.Blob.MediaType)
	if !matched {
		worker.finish(
			attempt,
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
		})
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
		})
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
		})
		return
	}
	if closeErr != nil {
		worker.fail(attempt, vault.UnderstandingDiagnostics{
			Error:    fmt.Sprintf("close source Blob: %v", closeErr),
			Stdout:   "",
			Stderr:   "",
			ExitCode: nil,
		})
		return
	}
	if worker.ctx.Err() != nil {
		return
	}

	_, requestBytes, err := understanding.BuildRequest(
		content,
		memory.Blob.MediaType,
		memory.ImportContext,
	)
	if err != nil {
		worker.fail(attempt, vault.UnderstandingDiagnostics{
			Error:    fmt.Sprintf("build plugin request: %v", err),
			Stdout:   "",
			Stderr:   "",
			ExitCode: nil,
		})
		return
	}

	execution := understanding.Run(worker.ctx, plugin.Command, requestBytes, nil)
	if worker.ctx.Err() != nil {
		return
	}
	diagnostics := vault.UnderstandingDiagnostics{
		Error:    execution.ProcessError,
		Stdout:   string(execution.Stdout),
		Stderr:   string(execution.Stderr),
		ExitCode: execution.ExitCode,
	}
	if execution.ProcessError != "" || execution.ExitCode == nil || *execution.ExitCode != 0 {
		if diagnostics.Error == "" {
			diagnostics.Error = "plugin process did not exit successfully"
		}
		worker.fail(attempt, diagnostics)
		return
	}

	result, err := understanding.ValidateResult(execution.Stdout)
	if err != nil {
		diagnostics.Error = "invalid plugin result: " + err.Error()
		worker.fail(attempt, diagnostics)
		return
	}

	artifacts := make([]vault.DerivedContent, 0, len(result.Artifacts))
	for _, artifact := range result.Artifacts {
		artifacts = append(artifacts, vault.DerivedContent{
			ID: uuid.Nil,
			Blob: vault.BlobInfo{
				Ref:       vault.Blobref{},
				MediaType: artifact.MediaType,
				ByteSize:  0,
			},
			Kind:       artifact.Kind,
			Content:    artifact.Content,
			Provenance: artifact.Provenance,
			Scope:      artifact.Scope,
		})
	}
	worker.finish(
		attempt,
		result.PluginVersion,
		artifacts,
		result.Warnings,
		diagnostics,
	)
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
	}, false
}

func (worker *understandingWorker) finish(
	attempt vault.UnderstandingAttempt,
	pluginVersion string,
	artifacts []vault.DerivedContent,
	warnings []string,
	diagnostics vault.UnderstandingDiagnostics,
) {
	if worker.ctx.Err() != nil {
		return
	}
	err := worker.memoryVault.FinishUnderstanding(
		worker.ctx,
		attempt,
		pluginVersion,
		artifacts,
		warnings,
	)
	if err == nil || worker.ctx.Err() != nil {
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
	worker.fail(attempt, diagnostics)
}

func (worker *understandingWorker) fail(
	attempt vault.UnderstandingAttempt,
	diagnostics vault.UnderstandingDiagnostics,
) {
	if worker.ctx.Err() != nil {
		return
	}
	if err := worker.memoryVault.FailUnderstanding(
		worker.ctx,
		attempt.ID,
		diagnostics,
	); err != nil && worker.ctx.Err() == nil {
		worker.logger.ErrorContext(
			worker.ctx,
			"fail understanding attempt persistence failed",
			"attempt_id",
			attempt.ID.String(),
			"error",
			err,
		)
	}
}
