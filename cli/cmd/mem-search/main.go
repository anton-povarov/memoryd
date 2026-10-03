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
	all := flags.Bool("all", false, "return every matching Memory")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: mem-search [--server ADDRESS] [-n N | --all] <quoted phrase>")
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	countProvided := false
	flags.Visit(func(parsed *flag.Flag) {
		countProvided = countProvided || parsed.Name == "n"
	})
	if flags.NArg() != 1 || strings.TrimSpace(flags.Arg(0)) == "" ||
		*count <= 0 || (*all && countProvided) {
		flags.Usage()
		return 2
	}
	serverURL := command.ServerURL(*server)
	var err error
	if *all {
		err = command.SearchAll(ctx, serverURL, flags.Arg(0), stdout)
	} else {
		err = command.Search(ctx, serverURL, flags.Arg(0), *count, stdout)
	}
	if err != nil {
		fmt.Fprintf(stderr, "mem-search: %v\n", err)
		return 1
	}
	return 0
}
