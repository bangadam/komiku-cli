package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestTUIDependenciesValidateOutput(t *testing.T) {
	dependencies, err := tuiDependencies("/tmp/manga", true)
	if err != nil || dependencies.OutputRoot != "/tmp/manga" {
		t.Fatalf("dependencies=%#v err=%v", dependencies, err)
	}
	if dependencies, err := tuiDependencies("", false); err != nil || dependencies.OutputRoot != "" {
		t.Fatalf("default dependencies=%#v err=%v", dependencies, err)
	}
	for _, test := range []struct {
		output string
		set    bool
		valid  bool
	}{{"", false, true}, {"", true, false}, {"  ", true, false}, {"  ", false, true}, {"a\x00b", true, false}} {
		_, err := tuiDependencies(test.output, test.set)
		if (err == nil) != test.valid {
			t.Fatalf("output=%q set=%v err=%v want valid=%v", test.output, test.set, err, test.valid)
		}
	}
}

func TestRootCommandExposesSharedCobraTree(t *testing.T) {
	var stdout, stderr bytes.Buffer
	root := newRootCommand(strings.NewReader(""), &stdout, &stderr)
	root.SetArgs([]string{"--help"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Available Commands:", "config", "dl", "pack", "tui"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("help missing %q:\n%s", want, stdout.String())
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("help stderr=%q", stderr.String())
	}
}

func TestRootCommandRoutesUnknownCommandsToConfiguredStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer
	root := newRootCommand(strings.NewReader(""), &stdout, &stderr)
	root.SetArgs([]string{"unknown"})
	err := root.ExecuteContext(context.Background())
	var coded exitError
	if !errors.As(err, &coded) || coded.code != 2 {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(stderr.String(), `unknown command "unknown"`) || stdout.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestUnknownCommandHintMatchesCommandTree(t *testing.T) {
	var stdout, stderr bytes.Buffer
	root := newRootCommand(strings.NewReader(""), &stdout, &stderr)
	root.SetArgs([]string{"frob"})
	if err := root.ExecuteContext(context.Background()); err == nil {
		t.Fatal("expected usage error")
	}
	hint := stderr.String()
	for _, want := range []string{"catalog", "config", "cover", "dl", "genre", "genres", "info", "latest", "library", "new", "pack", "random", "ranking", "read", "search", "tui", "trending", "update", "verify"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("unknown-command hint missing %q: %q", want, hint)
		}
	}
	if strings.Contains(hint, "send") {
		t.Fatalf("unknown-command hint mentions non-existent command \"send\": %q", hint)
	}
}

func TestTUICommandHelpDocumentsOutputFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	root := newRootCommand(strings.NewReader(""), &stdout, &stderr)
	root.SetArgs([]string{"tui", "--help"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Open the interactive TUI", "--out"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("tui help missing %q:\n%s", want, stdout.String())
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("tui help stderr=%q", stderr.String())
	}
}

func TestTUICommandRejectsPositionalArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	root := newRootCommand(strings.NewReader(""), &stdout, &stderr)
	root.SetArgs([]string{"tui", "extra-arg"})
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected cobra NoArgs usage error")
	}
	if !strings.Contains(err.Error(), `unknown command "extra-arg"`) {
		t.Fatalf("err = %v", err)
	}
	// The root command silences cobra's own reporting because main prints the
	// returned error, so nothing must leak into the configured writers here.
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	// With cobra reporting enabled the same failure surfaces as usage on stderr.
	var usageOut, usageErr bytes.Buffer
	verbose := newRootCommand(strings.NewReader(""), &usageOut, &usageErr)
	verbose.SilenceErrors, verbose.SilenceUsage = false, false
	verbose.SetArgs([]string{"tui", "extra-arg"})
	if err := verbose.ExecuteContext(context.Background()); err == nil {
		t.Fatal("expected cobra NoArgs usage error")
	}
	if !strings.Contains(usageErr.String(), `unknown command "extra-arg"`) {
		t.Fatalf("usage stderr=%q", usageErr.String())
	}
}
