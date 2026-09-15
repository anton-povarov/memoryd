package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/anton-povarov/memoryd/cli/internal/command"
	"github.com/google/uuid"
)

func main() {
	server := flag.String("server", "", "memoryd server URL")
	output := flag.String("o", "", "destination path, or - for stdout")
	force := flag.Bool("force", false, "replace an existing destination")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mem-get [--server URL] [-o PATH] [--force] <memory-id>")
		os.Exit(2)
	}
	memoryID, err := uuid.Parse(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "mem-get: invalid Memory UUID:", err)
		os.Exit(2)
	}
	if err := command.Get(context.Background(), command.ServerURL(*server), memoryID, *output, *force, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "mem-get:", err)
		os.Exit(1)
	}
}
