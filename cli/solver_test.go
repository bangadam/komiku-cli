package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeSolverScript(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell solver fixture is not portable to Windows")
	}
	path := filepath.Join(t.TempDir(), "solve.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNewChallengeSolverRunsScriptAndCachesSession(t *testing.T) {
	script := writeSolverScript(t, `printf '{"ua":"camoufox-ua","cookies":"cf_clearance=abc"}'`)
	sessionPath := filepath.Join(t.TempDir(), "cf-session.json")
	solve := NewChallengeSolver(script, sessionPath)

	session, err := solve(context.Background(), "https://komiku.org/dandadan-chapter-163/")
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	if session.Cookie != "cf_clearance=abc" || session.UserAgent != "camoufox-ua" || session.Host != "komiku.org" {
		t.Fatalf("session = %+v", session)
	}

	cached, err := LoadSession(sessionPath)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if cached == nil || cached.Cookie != "cf_clearance=abc" || cached.Host != "komiku.org" {
		t.Fatalf("cached = %+v", cached)
	}
}

func TestNewChallengeSolverReportsScriptFailure(t *testing.T) {
	script := writeSolverScript(t, "echo boom >&2\nexit 1")
	solve := NewChallengeSolver(script, "")
	_, err := solve(context.Background(), "https://komiku.org/x/")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want the script stderr", err)
	}
}

func TestNewChallengeSolverRejectsEmptyCookies(t *testing.T) {
	script := writeSolverScript(t, `printf '{"ua":"camoufox-ua","cookies":""}'`)
	solve := NewChallengeSolver(script, "")
	_, err := solve(context.Background(), "https://komiku.org/x/")
	if err == nil || !strings.Contains(err.Error(), "no cookies") {
		t.Fatalf("err = %v, want a no-cookies error", err)
	}
}

func TestSessionCachePathAndMissingSession(t *testing.T) {
	path := SessionCachePath(filepath.Join(t.TempDir(), "komiku-cli", "config.json"))
	if filepath.Base(path) != "cf-session.json" {
		t.Fatalf("cache path = %q", path)
	}
	session, err := LoadSession(path)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if session != nil {
		t.Fatalf("session = %+v, want nil", session)
	}
}

func TestResolveConfigKeepsSolverPath(t *testing.T) {
	config, err := ResolveConfig(FileConfig{Solver: "  tools/cf-solver/solve.py  "}, Overrides{})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if config.Solver != "tools/cf-solver/solve.py" {
		t.Fatalf("solver = %q", config.Solver)
	}
}
