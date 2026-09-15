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

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	global := flag.NewFlagSet("mem", flag.ContinueOnError)
	global.SetOutput(stderr)
	server := global.String("server", "", "memoryd server URL")
	global.Usage = func() {
		fmt.Fprintln(stderr, "usage: mem [--server ADDRESS] (get|put|info|list) [subcommand options]")
	}
	if err := global.Parse(args); err != nil {
		return 2
	}
	if global.NArg() == 0 {
		global.Usage()
		return 2
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
			fmt.Fprintln(stderr, "usage: mem [--server ADDRESS] get [-o PATH] [--force] <memory-id>")
		}
		if err := getFlags.Parse(remaining); err != nil {
			return 2
		}
		if getFlags.NArg() != 1 {
			getFlags.Usage()
			return 2
		}
		memoryID, parseErr := parseMemoryID(getFlags.Arg(0), stderr)
		if parseErr != nil {
			return 2
		}
		err = command.Get(ctx, serverURL, memoryID, *output, *force, stdout, stderr)
	case "put":
		putFlags := flag.NewFlagSet("mem put", flag.ContinueOnError)
		putFlags.SetOutput(stderr)
		putFlags.Usage = func() {
			fmt.Fprintln(stderr, "usage: mem [--server ADDRESS] put <path>")
		}
		if err := putFlags.Parse(remaining); err != nil {
			return 2
		}
		if putFlags.NArg() != 1 {
			putFlags.Usage()
			return 2
		}
		err = command.Put(ctx, serverURL, putFlags.Arg(0), stdout, stderr)
	case "info":
		infoFlags := flag.NewFlagSet("mem info", flag.ContinueOnError)
		infoFlags.SetOutput(stderr)
		infoFlags.Usage = func() {
			fmt.Fprintln(stderr, "usage: mem [--server ADDRESS] info <memory-id>")
		}
		if err := infoFlags.Parse(remaining); err != nil {
			return 2
		}
		if infoFlags.NArg() != 1 {
			infoFlags.Usage()
			return 2
		}
		memoryID, parseErr := parseMemoryID(infoFlags.Arg(0), stderr)
		if parseErr != nil {
			return 2
		}
		err = command.Info(ctx, serverURL, memoryID, stdout)
	case "list":
		listFlags := flag.NewFlagSet("mem list", flag.ContinueOnError)
		listFlags.SetOutput(stderr)
		count := listFlags.Int("n", 50, "number of Memories to return")
		all := listFlags.Bool("all", false, "return all Memories across pages")
		short := listFlags.Bool("short", false, "print only Memory IDs")
		listFlags.Usage = func() {
			fmt.Fprintln(stderr, "usage: mem [--server ADDRESS] list [-n N | --all] [--short]")
		}
		if err := listFlags.Parse(remaining); err != nil {
			return 2
		}
		if listFlags.NArg() != 0 || *count < 1 {
			listFlags.Usage()
			return 2
		}
		countSet := false
		listFlags.Visit(func(f *flag.Flag) {
			if f.Name == "n" {
				countSet = true
			}
		})
		if *all && countSet {
			fmt.Fprintln(stderr, "mem list: -n and --all cannot be used together")
			return 2
		}
		err = command.List(ctx, serverURL, *count, *all, *short, stdout)
	default:
		fmt.Fprintf(stderr, "mem: unknown subcommand %q\n", name)
		global.Usage()
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "mem %s: %v\n", name, err)
		return 1
	}
	return 0
}

func parseMemoryID(value string, stderr io.Writer) (uuid.UUID, error) {
	memoryID, err := uuid.Parse(value)
	if err != nil {
		fmt.Fprintln(stderr, "mem: invalid Memory UUID:", err)
	}
	return memoryID, err
}
