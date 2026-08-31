package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bangadam/komiku-cli/cli"
	"github.com/bangadam/komiku-cli/komiku"
	packer "github.com/bangadam/komiku-cli/pack"
	"github.com/bangadam/komiku-cli/store"
)

type Dependencies struct {
	HTTP       *http.Client
	ConfigPath string
	OutputRoot string
	BaseURL    string
	Now        func() time.Time
	Workers    int
	Getenv     func(string) string
}

func Run(ctx context.Context, input io.Reader, output, stderr io.Writer, dependencies Dependencies) int {
	configPath := dependencies.ConfigPath
	var err error
	if configPath == "" {
		configPath, err = cli.ConfigPath()
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
	}
	fileConfig, err := cli.LoadFileConfig(configPath)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	overrides := cli.Overrides{}
	if dependencies.OutputRoot != "" {
		overrides.OutputRoot = &dependencies.OutputRoot
	}
	config, err := cli.ResolveConfig(fileConfig, overrides)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	client := komiku.NewClient(dependencies.HTTP, config.ImageDelay)
	if dependencies.BaseURL != "" {
		client.BaseURL = dependencies.BaseURL
	}
	workers := dependencies.Workers
	if workers == 0 {
		workers = 3
	}
	backend := &realBackend{client: client, config: config, configFile: fileConfig, configPath: configPath, workers: workers, now: dependencies.Now}
	if dependencies.OutputRoot != "" {
		resolved, err := backend.SetOutputRoot(config.OutputRoot, false)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		config.OutputRoot = resolved
	}
	getenv := dependencies.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	plain := getenv("NO_COLOR") != "" || strings.EqualFold(getenv("TERM"), "dumb")
	programOutput := output
	if plain {
		programOutput = &ansiFreeWriter{output: output}
	}
	renderer := lipgloss.NewRenderer(programOutput)
	inner := newModel(backend, packer.Preset(config.Preset), plain, renderer, dependencies.Now)
	inner.outputRoot = config.OutputRoot
	inner.beginHome(config.OutputRoot)
	options := []tea.ProgramOption{tea.WithInput(input), tea.WithOutput(programOutput), tea.WithoutSignalHandler()}
	if !plain {
		options = append(options, tea.WithAltScreen())
	}
	program := tea.NewProgram(inner, options...)
	err = runProgram(ctx, program)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}

type programRunner interface {
	Send(tea.Msg)
	Run() (tea.Model, error)
}

func runProgram(ctx context.Context, program programRunner) error {
	programDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			program.Send(shutdownMsg{})
		case <-programDone:
		}
	}()
	_, err := program.Run()
	close(programDone)
	return err
}

type ansiFreeWriter struct {
	mu     sync.Mutex
	output io.Writer
	state  uint8
}

func (w *ansiFreeWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	filtered := make([]byte, 0, len(data))
	for _, value := range data {
		switch w.state {
		case 0:
			if value == 0x1b {
				w.state = 1
			} else {
				filtered = append(filtered, value)
			}
		case 1:
			switch value {
			case '[':
				w.state = 2
			case ']':
				w.state = 3
			default:
				w.state = 0
			}
		case 2:
			if value >= 0x40 && value <= 0x7e {
				w.state = 0
			}
		case 3:
			if value == 0x07 {
				w.state = 0
			} else if value == 0x1b {
				w.state = 4
			}
		case 4:
			if value == '\\' {
				w.state = 0
			} else if value != 0x1b {
				w.state = 3
			}
		}
	}
	if len(filtered) > 0 {
		if _, err := w.output.Write(filtered); err != nil {
			return 0, err
		}
	}
	return len(data), nil
}

type realBackend struct {
	client     *komiku.Client
	config     cli.Config
	configFile cli.FileConfig
	configPath string
	workers    int
	now        func() time.Time
	store      *store.SeriesStore
	series     string
}

func (b *realBackend) SetOutputRoot(path string, persist bool) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	path, err = normalizeOutputRoot(path, home)
	if err != nil {
		return "", err
	}
	overrides := cli.Overrides{OutputRoot: &path}
	config, err := cli.ResolveConfig(b.configFile, overrides)
	if err != nil {
		return "", err
	}
	if persist {
		next := b.configFile
		next.OutputRoot = path
		if err := cli.SaveFileConfig(b.configPath, next); err != nil {
			return "", fmt.Errorf("save default storage location: %w", err)
		}
		b.configFile = next
	}
	b.config.OutputRoot = config.OutputRoot
	b.store, b.series = nil, ""
	return path, nil
}

func normalizeOutputRoot(path, home string) (string, error) {
	path = strings.TrimSpace(path)
	switch {
	case path == "~":
		path = home
	case strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`):
		path = filepath.Join(home, path[2:])
	case strings.HasPrefix(path, "~"):
		return "", errors.New("only ~/ home expansion is supported")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve storage location: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", fmt.Errorf("create storage location: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("inspect storage location: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("storage location is not a directory")
	}
	return filepath.Clean(abs), nil
}

func (b *realBackend) DownloadedSeries(outputRoot string) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("find home directory: %w", err)
	}
	root, err := normalizeExistingDirectory(outputRoot, home)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read storage location: %w", err)
	}
	series := make([]string, 0)
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if _, err := os.Lstat(filepath.Join(dir, ".state.json")); err == nil {
			series = append(series, dir)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect downloaded manga %s: %w", entry.Name(), err)
		}
	}
	sort.Strings(series)
	return series, nil
}

func (b *realBackend) PackNeedsRecovery(seriesDir string) (bool, error) {
	info, err := os.Lstat(cli.PackManifestPath(seriesDir))
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect pack metadata: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("pack metadata is not a regular file")
	}
	return false, nil
}

func (b *realBackend) PackDownloaded(ctx context.Context, seriesDir string, recover bool, preset packer.Preset) (string, error) {
	var output bytes.Buffer
	err := cli.PackDownloaded(ctx, seriesDir, cli.PackDownloadedOptions{
		Preset:           preset,
		RecoverWikipedia: recover,
		RecoverComplete:  recover,
		HTTP:             b.client.HTTP,
		Output:           &output,
	})
	return strings.TrimSpace(output.String()), err
}

func normalizeExistingDirectory(path, home string) (string, error) {
	path = strings.TrimSpace(path)
	switch {
	case path == "~":
		path = home
	case strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`):
		path = filepath.Join(home, path[2:])
	case strings.HasPrefix(path, "~"):
		return "", errors.New("only ~/ home expansion is supported")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve directory: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("inspect directory: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("path is not a directory")
	}
	return filepath.Clean(abs), nil
}

func (b *realBackend) Search(ctx context.Context, query string) ([]komiku.Series, error) {
	return b.client.Search(ctx, query)
}

func (b *realBackend) Discover(ctx context.Context, seriesURL string) ([]komiku.Chapter, error) {
	return b.client.Discover(ctx, seriesURL)
}

func (b *realBackend) ensureStore(seriesURL string) (*store.SeriesStore, string, error) {
	parsed, err := url.Parse(seriesURL)
	if err != nil {
		return nil, "", err
	}
	series := cli.SeriesSlug(parsed)
	if series == "" {
		return nil, "", fmt.Errorf("cannot derive series directory from %q", seriesURL)
	}
	if b.store != nil && b.series == series {
		return b.store, series, nil
	}
	seriesStore, err := store.Open(b.config.OutputRoot, series)
	if err != nil {
		return nil, "", err
	}
	b.store, b.series = seriesStore, series
	return seriesStore, series, nil
}

func (b *realBackend) LoadVolumes(ctx context.Context, seriesURL string, chapters []komiku.Chapter) ([]komiku.Volume, error) {
	seriesStore, series, err := b.ensureStore(seriesURL)
	if err != nil {
		return nil, err
	}
	return cli.LoadVolumeMapping(ctx, b.client, seriesStore.Dir(), series, chapters)
}
func (b *realBackend) LoadWikipediaVolumes(ctx context.Context, seriesURL string) ([]komiku.Volume, error) {
	parsed, err := url.Parse(seriesURL)
	if err != nil {
		return nil, err
	}
	slug := cli.SeriesSlug(parsed)
	if slug == "" {
		return nil, fmt.Errorf("cannot derive Wikipedia title from %q", seriesURL)
	}
	words := strings.Fields(strings.NewReplacer("-", " ", "_", " ").Replace(slug))
	for index, word := range words {
		runes := []rune(word)
		if len(runes) > 0 {
			runes[0] = unicode.ToUpper(runes[0])
			words[index] = string(runes)
		}
	}
	return b.client.FetchWikipediaDisplayVolumes(ctx, strings.Join(words, " "))
}

func (b *realBackend) SaveManualVolumes(seriesURL string, volumes []komiku.Volume, maxChapter int) error {
	seriesStore, _, err := b.ensureStore(seriesURL)
	if err != nil {
		return err
	}
	return cli.SaveManualVolumeMapping(seriesStore.Dir(), volumes, maxChapter)
}

func (b *realBackend) Start(ctx context.Context, seriesURL string, jobs []cli.Job) (batchRun, error) {
	seriesStore, _, err := b.ensureStore(seriesURL)
	if err != nil {
		return nil, err
	}
	engine := &cli.Engine{Client: b.client, Store: seriesStore, Workers: b.workers, Now: b.now}
	return engine.Start(ctx, jobs), nil
}

func (b *realBackend) RecordPackManifest(seriesURL, provenance string, mappings []komiku.Volume, jobs []cli.Job, results []cli.Result) error {
	seriesStore, series, err := b.ensureStore(seriesURL)
	if err != nil {
		return err
	}
	return cli.RecordPackManifest(seriesStore.Dir(), series, seriesURL, provenance, mappings, jobs, results)
}

func (b *realBackend) PreparePack(seriesURL string, preset packer.Preset, mappings []komiku.Volume, jobs []cli.Job, results []cli.Result) cli.PackPlan {
	seriesStore, series, err := b.ensureStore(seriesURL)
	if err != nil {
		return cli.PackPlan{Preset: preset, DisabledReason: err.Error()}
	}
	return cli.PreparePack(seriesStore, series, preset, mappings, jobs, results)
}

func (b *realBackend) Pack(ctx context.Context, plan cli.PackPlan) ([]cli.PackOutcome, error) {
	return cli.PackPreparedVolumes(ctx, plan)
}
