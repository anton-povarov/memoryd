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
	"github.com/anton-povarov/memoryd/server/internal/httpapi"
	"github.com/anton-povarov/memoryd/server/internal/logging"
	"github.com/anton-povarov/memoryd/server/internal/server"
	"github.com/anton-povarov/memoryd/server/internal/vault"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "memoryd:", err)
		os.Exit(1)
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

	memoryVault, err := vault.Open(context.Background(), cfg.Storage.DatabasePath, cfg.Storage.BlobDir)
	if err != nil {
		return err
	}
	defer memoryVault.Close()

	httpHandler := httpapi.NewHandler(version, cfg.Storage.DataDir, memoryVault)
	httpServer, err := server.New(cfg, logger, httpHandler)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return httpServer.Run(ctx)
}
