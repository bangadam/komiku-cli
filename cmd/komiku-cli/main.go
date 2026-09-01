package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/bangadam/komiku-cli/cli"
	"github.com/bangadam/komiku-cli/tui"
)

// exitError carries an already-reported terminal exit code.
type exitError struct {
	code int
}

func (e exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root := newRootCommand(os.Stdin, os.Stdout, os.Stderr)
	if err := root.ExecuteContext(ctx); err != nil {
		var coded exitError
		if errors.As(err, &coded) {
			os.Exit(coded.code)
		}
		fmt.Fprintln(root.ErrOrStderr(), "error:", err)
		os.Exit(1)
	}
}

func newRootCommand(input io.Reader, stdout, stderr io.Writer) *cobra.Command {
	root := cli.NewRootCommand(stdout, stderr, cli.Dependencies{})
	root.SetIn(input)
	root.Long = "komiku-cli downloads Komiku manga chapters and packs them into offline CBZ archives. Run without arguments for the interactive TUI."
	var output string
	root.Args = cobra.ArbitraryArgs
	root.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			fmt.Fprintf(cmd.ErrOrStderr(), "unknown command %q; expected tui, dl, pack, or config\n", args[0])
			return exitError{2}
		}
		dependencies, err := tuiDependencies(output, cmd.Flags().Changed("out"))
		if err != nil {
			fmt.Fprintln(cmd.ErrOrStderr(), "error:", err)
			return exitError{2}
		}
		return runTUI(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), dependencies)
	}
	root.Flags().StringVar(&output, "out", "", "output directory for this run")
	root.AddCommand(newTUICommand())
	return root
}

func newTUICommand() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Open the interactive TUI",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dependencies, err := tuiDependencies(output, cmd.Flags().Changed("out"))
			if err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "error:", err)
				return exitError{2}
			}
			return runTUI(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), dependencies)
		},
	}
	cmd.Flags().StringVar(&output, "out", "", "output directory for this run")
	return cmd
}

func tuiDependencies(output string, set bool) (tui.Dependencies, error) {
	if set && (strings.ContainsRune(output, 0) || strings.TrimSpace(output) == "") {
		return tui.Dependencies{}, errors.New("--out must be a non-empty directory")
	}
	return tui.Dependencies{OutputRoot: output}, nil
}

func runTUI(ctx context.Context, input io.Reader, stdout, stderr io.Writer, dependencies tui.Dependencies) error {
	code := tui.Run(ctx, input, stdout, stderr, dependencies)
	if code == 0 {
		return nil
	}
	return exitError{code}
}
