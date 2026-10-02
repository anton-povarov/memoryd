package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/anton-povarov/memoryd/cli/internal/command"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mem-search", flag.ContinueOnError)
	flags.SetOutput(stderr)
	server := flags.String("server", "", "memoryd server URL")
	count := flags.Int64("n", 50, "maximum number of Memories to return")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: mem-search [--server ADDRESS] [-n N] <quoted phrase>")
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 || strings.TrimSpace(flags.Arg(0)) == "" || *count <= 0 {
		flags.Usage()
		return 2
	}
	if err := command.Search(
		ctx,
		command.ServerURL(*server),
		flags.Arg(0),
		*count,
		stdout,
	); err != nil {
		fmt.Fprintf(stderr, "mem-search: %v\n", err)
		return 1
	}
	return 0
}
