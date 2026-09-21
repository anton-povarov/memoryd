package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/anton-povarov/memoryd/server/internal/config"
	"github.com/anton-povarov/memoryd/server/internal/logging"
	"github.com/anton-povarov/memoryd/server/internal/server"
	"github.com/anton-povarov/memoryd/server/internal/vault"
)

var version = "dev"

const exitFailure = 1

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "memoryd:", err)
		os.Exit(exitFailure)
	}
}

func run() error {
	configPath := flag.String("c", "", "path to a memoryd YAML configuration file")
	flag.Parse()

	cfg := config.Defaults()
	var err error
	if *configPath != "" {
		cfg, err = config.Load(*configPath)
		if err != nil {
			return err
		}
	}

	logger, err := logging.New(cfg.Logging)
	if err != nil {
		return err
	}
	slog.SetDefault(logger)

	memoryVault, err := vault.Open(
		context.Background(),
		logger,
		cfg.Storage.DatabasePath,
		cfg.Storage.BlobDir,
		cfg.Storage.UploadDir,
	)
	if err != nil {
		return err
	}
	defer memoryVault.Close() // nolint:errcheck

	httpServer, err := server.New(version, cfg, logger, memoryVault)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return httpServer.Run(ctx)
}
