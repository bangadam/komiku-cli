package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/bangadam/komiku-cli/cli"
	"github.com/bangadam/komiku-cli/tui"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) == 1 {
		os.Exit(tui.Run(ctx, os.Stdin, os.Stdout, os.Stderr, tui.Dependencies{}))
	}
	args := os.Args[1:]
	if args[0] == "tui" || strings.HasPrefix(args[0], "-") {
		if args[0] == "tui" {
			args = args[1:]
		}
		dependencies, err := parseTUIArgs(args)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(2)
		}
		os.Exit(tui.Run(ctx, os.Stdin, os.Stdout, os.Stderr, dependencies))
	}
	os.Exit(cli.Main(ctx, args, os.Stdout, os.Stderr, cli.Dependencies{}))
}

func parseTUIArgs(args []string) (tui.Dependencies, error) {
	flags := flag.NewFlagSet("tui", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var output string
	flags.StringVar(&output, "out", "", "output directory for this run")
	if err := flags.Parse(args); err != nil {
		return tui.Dependencies{}, err
	}
	if flags.NArg() != 0 {
		return tui.Dependencies{}, fmt.Errorf("unexpected positional argument %q", flags.Arg(0))
	}
	outputSet := false
	flags.Visit(func(flag *flag.Flag) {
		outputSet = outputSet || flag.Name == "out"
	})
	if outputSet && (strings.TrimSpace(output) == "" || strings.IndexByte(output, 0) >= 0) {
		return tui.Dependencies{}, errors.New("--out must be a non-empty directory")
	}
	return tui.Dependencies{OutputRoot: output}, nil
}
