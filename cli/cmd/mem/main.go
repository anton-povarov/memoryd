package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/anton-povarov/memoryd/cli/internal/command"
	"github.com/google/uuid"
)

const (
	exitSuccess      = 0
	exitFailure      = 1
	exitUsage        = 2
	defaultListCount = 50
	minimumListCount = 1
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	global := flag.NewFlagSet("mem", flag.ContinueOnError)
	global.SetOutput(stderr)
	server := global.String("server", "", "memoryd server URL")
	global.Usage = func() {
		fmt.Fprintln(
			stderr,
			"usage: mem [--server ADDRESS] (get|put|info|list) [subcommand options]",
		)
	}
	if err := global.Parse(args); err != nil {
		return exitUsage
	}
	if global.NArg() == 0 {
		global.Usage()
		return exitUsage
	}

	name := global.Arg(0)
	remaining := global.Args()[1:]
	serverURL := command.ServerURL(*server)
	var err error
	switch name {
	case "get":
		getFlags := flag.NewFlagSet("mem get", flag.ContinueOnError)
		getFlags.SetOutput(stderr)
		output := getFlags.String("o", "", "destination path, or - for stdout")
		force := getFlags.Bool("force", false, "replace an existing destination")
		getFlags.Usage = func() {
			fmt.Fprintln(
				stderr,
				"usage: mem [--server ADDRESS] get [-o PATH] [--force] <memory-id>",
			)
		}
		if err := getFlags.Parse(remaining); err != nil {
			return exitUsage
		}
		if getFlags.NArg() != 1 {
			getFlags.Usage()
			return exitUsage
		}
		memoryID, parseErr := parseMemoryID(getFlags.Arg(0), stderr)
		if parseErr != nil {
			return exitUsage
		}
		err = command.Get(ctx, serverURL, memoryID, *output, *force, stdout, stderr)
	case "put":
		putFlags := flag.NewFlagSet("mem put", flag.ContinueOnError)
		putFlags.SetOutput(stderr)
		putFlags.Usage = func() {
			fmt.Fprintln(stderr, "usage: mem [--server ADDRESS] put <path>")
		}
		if err := putFlags.Parse(remaining); err != nil {
			return exitUsage
		}
		if putFlags.NArg() != 1 {
			putFlags.Usage()
			return exitUsage
		}
		err = command.Put(ctx, serverURL, putFlags.Arg(0), stdout, stderr)
	case "info":
		infoFlags := flag.NewFlagSet("mem info", flag.ContinueOnError)
		infoFlags.SetOutput(stderr)
		infoFlags.Usage = func() {
			fmt.Fprintln(stderr, "usage: mem [--server ADDRESS] info <memory-id>")
		}
		if err := infoFlags.Parse(remaining); err != nil {
			return exitUsage
		}
		if infoFlags.NArg() != 1 {
			infoFlags.Usage()
			return exitUsage
		}
		memoryID, parseErr := parseMemoryID(infoFlags.Arg(0), stderr)
		if parseErr != nil {
			return exitUsage
		}
		err = command.Info(ctx, serverURL, memoryID, stdout)
	case "list":
		listFlags := flag.NewFlagSet("mem list", flag.ContinueOnError)
		listFlags.SetOutput(stderr)
		count := listFlags.Int("n", defaultListCount, "number of Memories to return")
		all := listFlags.Bool("all", false, "return all Memories across pages")
		short := listFlags.Bool("short", false, "print only Memory IDs")
		listFlags.Usage = func() {
			fmt.Fprintln(stderr, "usage: mem [--server ADDRESS] list [-n N | --all] [--short]")
		}
		if err := listFlags.Parse(remaining); err != nil {
			return exitUsage
		}
		if listFlags.NArg() != 0 || *count < minimumListCount {
			listFlags.Usage()
			return exitUsage
		}
		countSet := false
		listFlags.Visit(func(f *flag.Flag) {
			if f.Name == "n" {
				countSet = true
			}
		})
		if *all && countSet {
			fmt.Fprintln(stderr, "mem list: -n and --all cannot be used together")
			return exitUsage
		}
		err = command.List(ctx, serverURL, *count, *all, *short, stdout)
	default:
		fmt.Fprintf(stderr, "mem: unknown subcommand %q\n", name)
		global.Usage()
		return exitUsage
	}
	if err != nil {
		fmt.Fprintf(stderr, "mem %s: %v\n", name, err)
		return exitFailure
	}
	return exitSuccess
}

func parseMemoryID(value string, stderr io.Writer) (uuid.UUID, error) {
	memoryID, err := uuid.Parse(value)
	if err != nil {
		fmt.Fprintln(stderr, "mem: invalid Memory UUID:", err)
	}
	return memoryID, err
}
