package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/anton-povarov/memoryd/cli/internal/command"
)

func main() {
	server := flag.String("server", "", "memoryd server URL")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mem-put [--server URL] <path>")
		os.Exit(2)
	}
	if err := command.Put(context.Background(), command.ServerURL(*server), flag.Arg(0), os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "mem-put:", err)
		os.Exit(1)
	}
}
