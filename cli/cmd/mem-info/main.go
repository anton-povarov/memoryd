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
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mem-info [--server URL] <memory-id>")
		os.Exit(2)
	}
	memoryID, err := uuid.Parse(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "mem-info: invalid Memory UUID:", err)
		os.Exit(2)
	}
	if err := command.Info(context.Background(), command.ServerURL(*server), memoryID, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "mem-info:", err)
		os.Exit(1)
	}
}
