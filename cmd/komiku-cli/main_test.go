package main

import "testing"

func TestParseTUIArgsOutput(t *testing.T) {
	dependencies, err := parseTUIArgs([]string{"--out", "/tmp/manga"})
	if err != nil || dependencies.OutputRoot != "/tmp/manga" {
		t.Fatalf("dependencies=%#v err=%v", dependencies, err)
	}
	if dependencies, err := parseTUIArgs(nil); err != nil || dependencies.OutputRoot != "" {
		t.Fatalf("default dependencies=%#v err=%v", dependencies, err)
	}
	for _, args := range [][]string{{"--out", ""}, {"--unknown"}, {"--out", "/tmp/manga", "extra"}} {
		if _, err := parseTUIArgs(args); err == nil {
			t.Fatalf("invalid args accepted: %#v", args)
		}
	}
}
