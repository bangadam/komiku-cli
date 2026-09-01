package tui

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bangadam/komiku-cli/cli"
	"github.com/bangadam/komiku-cli/komiku"
	packer "github.com/bangadam/komiku-cli/pack"
)

func TestANSIFreeWriterStripsSplitControlSequences(t *testing.T) {
	var output bytes.Buffer
	writer := &ansiFreeWriter{output: &output}
	for _, chunk := range [][]byte{
		[]byte("before\x1b["),
		[]byte("31mred\x1b]0;title"),
		[]byte("\x07after\x1b[2K"),
	} {
		if written, err := writer.Write(chunk); err != nil || written != len(chunk) {
			t.Fatalf("written=%d want=%d err=%v", written, len(chunk), err)
		}
	}
	if got := output.String(); got != "beforeredafter" {
		t.Fatalf("filtered output=%q", got)
	}
}

type tuiRoundTripFunc func(*http.Request) (*http.Response, error)

func (f tuiRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestRealBackendFindsOnlyDownloadedSeriesAndDetectsRecovery(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "sakamoto-days")
	manifest := filepath.Join(root, "manifest-series")
	other := filepath.Join(root, "notes")
	for _, dir := range []string{legacy, manifest, other} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{legacy, manifest} {
		if err := os.WriteFile(filepath.Join(dir, ".state.json"), []byte(`{"done":[]}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(cli.PackManifestPath(manifest), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	backend := &realBackend{}
	series, err := backend.DownloadedSeries(root)
	if err != nil || len(series) != 2 || series[0] != manifest || series[1] != legacy {
		t.Fatalf("series=%#v err=%v", series, err)
	}
	if recover, err := backend.PackNeedsRecovery(legacy); err != nil || !recover {
		t.Fatalf("legacy recover=%v err=%v", recover, err)
	}
	if recover, err := backend.PackNeedsRecovery(manifest); err != nil || recover {
		t.Fatalf("manifest recover=%v err=%v", recover, err)
	}
}

func TestNormalizeOutputRootExpandsHomeAndRejectsFiles(t *testing.T) {
	home := t.TempDir()
	got, err := normalizeOutputRoot(filepath.Join("~", "Manga"), home)
	if err != nil || got != filepath.Join(home, "Manga") {
		t.Fatalf("path=%q err=%v", got, err)
	}
	info, err := os.Stat(got)
	if err != nil || !info.IsDir() {
		t.Fatalf("output directory not prepared: info=%v err=%v", info, err)
	}
	file := filepath.Join(home, "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := normalizeOutputRoot(file, home); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("output file accepted: %v", err)
	}
	if _, err := normalizeOutputRoot("~someone/Manga", home); err == nil {
		t.Fatal("unsupported home expansion accepted")
	}
}

func TestRealBackendOutputRootSessionAndPersistentChanges(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config", "config.json")
	oldRoot := filepath.Join(root, "old")
	sessionRoot := filepath.Join(root, "session")
	defaultRoot := filepath.Join(root, "default")
	fileConfig := cli.FileConfig{OutputRoot: oldRoot, ImageDelay: "1s", Preset: "raw"}
	if err := cli.SaveFileConfig(configPath, fileConfig); err != nil {
		t.Fatal(err)
	}
	backend := &realBackend{config: cli.Config{OutputRoot: oldRoot, ImageDelay: time.Second, Preset: "raw"}, configFile: fileConfig, configPath: configPath}
	if resolved, err := backend.SetOutputRoot(sessionRoot, false); err != nil || resolved != sessionRoot {
		t.Fatalf("resolved=%q err=%v", resolved, err)
	}
	unchanged, err := cli.LoadFileConfig(configPath)
	if err != nil || unchanged.OutputRoot != oldRoot {
		t.Fatalf("session change persisted: config=%#v err=%v", unchanged, err)
	}
	if resolved, err := backend.SetOutputRoot(defaultRoot, true); err != nil || resolved != defaultRoot {
		t.Fatalf("resolved=%q err=%v", resolved, err)
	}
	saved, err := cli.LoadFileConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if backend.config.OutputRoot != defaultRoot || saved.OutputRoot != defaultRoot || saved.ImageDelay != "1s" || saved.Preset != "raw" {
		t.Fatalf("backend=%#v saved=%#v", backend.config, saved)
	}
}

func TestRealBackendSetPresetPersistsConfig(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config", "config.json")
	fileConfig := cli.FileConfig{OutputRoot: filepath.Join(root, "manga"), ImageDelay: "1s", Preset: "raw"}
	if err := cli.SaveFileConfig(configPath, fileConfig); err != nil {
		t.Fatal(err)
	}
	backend := &realBackend{config: cli.Config{OutputRoot: fileConfig.OutputRoot, ImageDelay: time.Second, Preset: "raw"}, configFile: fileConfig, configPath: configPath}
	if err := backend.SetPreset(packer.Tiny); err != nil {
		t.Fatalf("SetPreset: %v", err)
	}
	if backend.config.Preset != "tiny" || backend.configFile.Preset != "tiny" {
		t.Fatalf("backend preset not updated: %#v %#v", backend.config, backend.configFile)
	}
	saved, err := cli.LoadFileConfig(configPath)
	if err != nil || saved.Preset != "tiny" || saved.OutputRoot != fileConfig.OutputRoot || saved.ImageDelay != "1s" {
		t.Fatalf("saved=%#v err=%v", saved, err)
	}
}

func TestRealBackendWikipediaVolumesIgnoresDownloadMappingCache(t *testing.T) {
	outputRoot := t.TempDir()
	cachePath := filepath.Join(outputRoot, "sakamoto-days", ".volumes.json")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatal(err)
	}
	const sentinel = `{"source":"manual-tui","volumes":[{"volume":99,"start":1,"end":1}]}`
	if err := os.WriteFile(cachePath, []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}
	requests := 0
	httpClient := &http.Client{Transport: tuiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.String() != "https://en.wikipedia.org/wiki/List_of_Sakamoto_Days_chapters" {
			t.Fatalf("Wikipedia URL = %s", request.URL)
		}
		body := `<section aria-labelledby="Volumes"><th scope="row" id="vol1">1</th><li>Days 1: one</li></section>`
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	backend := &realBackend{client: komiku.NewClient(httpClient, 0), config: cli.Config{OutputRoot: outputRoot}}

	volumes, err := backend.LoadWikipediaVolumes(context.Background(), "https://fixture.invalid/manga/sakamoto-days/")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || len(volumes) != 1 || volumes[0].Volume != 1 {
		t.Fatalf("requests=%d volumes=%#v", requests, volumes)
	}
	data, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != sentinel {
		t.Fatalf("display lookup changed download mapping cache: %q", data)
	}
}

type blockingProgram struct {
	started chan struct{}
	sent    chan tea.Msg
	release chan struct{}
}

func (p *blockingProgram) Send(message tea.Msg) { p.sent <- message }

func (p *blockingProgram) Run() (tea.Model, error) {
	close(p.started)
	<-p.release
	return nil, nil
}

func TestRunProgramContextBridgeWaitsForGracefulCompletion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	program := &blockingProgram{
		started: make(chan struct{}),
		sent:    make(chan tea.Msg, 1),
		release: make(chan struct{}),
	}
	done := make(chan error, 1)
	go func() { done <- runProgram(ctx, program) }()
	<-program.started
	cancel()
	select {
	case message := <-program.sent:
		if _, ok := message.(shutdownMsg); !ok {
			t.Fatalf("context sent %T, want shutdownMsg", message)
		}
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not reach program")
	}
	select {
	case <-done:
		t.Fatal("runProgram returned before graceful program completion")
	case <-time.After(20 * time.Millisecond):
	}
	close(program.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("runProgram did not return after graceful completion")
	}
}
