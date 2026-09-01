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
