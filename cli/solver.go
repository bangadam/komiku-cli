package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bangadam/komiku-cli/komiku"
	"github.com/bangadam/komiku-cli/store"
)

const (
	// solverTimeout bounds one browser attempt, including the challenge wait.
	solverTimeout   = 180 * time.Second
	maxSolverOutput = 1 << 20
	sessionFileName = "cf-session.json"
)

// sessionFile persists the browser session a solver obtained. cf_clearance is
// bound to the IP address and User-Agent that solved it, so both are stored.
type sessionFile struct {
	komiku.SolvedSession
	SavedAt time.Time `json:"saved_at"`
}

// SessionCachePath names the challenge session cache next to the persistent
// config file.
func SessionCachePath(configPath string) string {
	if configPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(configPath), sessionFileName)
}

// LoadSession returns the cached session, or nil when none has been saved.
func LoadSession(path string) (*komiku.SolvedSession, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read challenge session: %w", err)
	}
	var cached sessionFile
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, fmt.Errorf("invalid challenge session: %w", err)
	}
	if strings.TrimSpace(cached.Cookie) == "" {
		return nil, nil
	}
	session := cached.SolvedSession
	return &session, nil
}

func saveSession(path string, session komiku.SolvedSession) error {
	if path == "" {
		return nil
	}
	return store.WriteJSONAtomic(path, sessionFile{SolvedSession: session, SavedAt: time.Now()})
}

// solverOutput is the JSON contract printed by a challenge solver:
// {"ua": "...", "cookies": "cf_clearance=..."}.
type solverOutput struct {
	UserAgent string `json:"ua"`
	Cookies   string `json:"cookies"`
}

// ResolveSolverPath prefers the configured path and otherwise looks for the
// bundled Camoufox script next to the binary or in the working tree.
func ResolveSolverPath(configured string) string {
	if path := strings.TrimSpace(configured); path != "" {
		return path
	}
	candidates := make([]string, 0, 3)
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "tools", "cf-solver", "solve.py"))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(cwd, "tools", "cf-solver", "solve.py"),
			filepath.Join(cwd, "..", "tools", "cf-solver", "solve.py"),
		)
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// NewChallengeSolver builds the SolveFunc the HTTP client calls when it meets
// a Cloudflare challenge. Successful sessions are cached so later runs reuse
// the cookie instead of launching a browser again.
func NewChallengeSolver(solverPath, sessionPath string) func(context.Context, string) (komiku.SolvedSession, error) {
	return func(ctx context.Context, target string) (komiku.SolvedSession, error) {
		runCtx, cancel := context.WithTimeout(ctx, solverTimeout)
		defer cancel()
		var cmd *exec.Cmd
		if strings.HasSuffix(strings.ToLower(solverPath), ".py") {
			cmd = exec.CommandContext(runCtx, "python3", solverPath, target)
		} else {
			cmd = exec.CommandContext(runCtx, solverPath, target)
		}
		stdout := &limitedBuffer{limit: maxSolverOutput}
		stderr := &limitedBuffer{limit: maxSolverOutput}
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		if err := cmd.Run(); err != nil {
			message := strings.TrimSpace(stderr.String())
			if message == "" {
				message = err.Error()
			} else {
				message = err.Error() + ": " + message
			}
			return komiku.SolvedSession{}, fmt.Errorf("run %s: %s", solverPath, message)
		}
		var out solverOutput
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			return komiku.SolvedSession{}, fmt.Errorf("parse solver output: %w", err)
		}
		cookie := strings.TrimSpace(out.Cookies)
		if cookie == "" {
			return komiku.SolvedSession{}, errors.New("solver returned no cookies")
		}
		host := ""
		if parsed, err := url.Parse(target); err == nil {
			host = parsed.Hostname()
		}
		if host == "" {
			return komiku.SolvedSession{}, fmt.Errorf("cannot determine host for %q", target)
		}
		session := komiku.SolvedSession{UserAgent: strings.TrimSpace(out.UserAgent), Cookie: cookie, Host: host}
		// The session is usable for this run even if the cache write fails;
		// caching only saves a future solve, so the write must not fail the
		// fetch that already succeeded.
		_ = saveSession(sessionPath, session)
		return session, nil
	}
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, errors.New("solver output too large")
	}
	return b.Buffer.Write(p)
}
