package tui

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bangadam/komiku-cli/cli"
	"github.com/bangadam/komiku-cli/komiku"
	packer "github.com/bangadam/komiku-cli/pack"
)

type screen uint8

const (
	homeScreen screen = iota
	outputScreen
	searchScreen
	chaptersScreen
	rangeScreen
	downloadingScreen
	doneScreen
	packingScreen
	packSeriesScreen
	packPathScreen
	packRecoveryScreen
	standalonePackingScreen
	standalonePackDoneScreen
)

type rangeMode uint8

const (
	chapterRange rangeMode = iota
	volumeRange
	manualRange
	flatRange
)

type groupFocusKind uint8

const (
	groupFocusNone groupFocusKind = iota
	groupFocusVolume
	groupFocusChapter
)

type groupFocus struct {
	kind       groupFocusKind
	volume     int
	chapterURL string
}

type groupedRowKind uint8

const (
	groupedVolumeRow groupedRowKind = iota
	groupedUnmappedRow
	groupedChapterRow
)

type groupedRow struct {
	kind         groupedRowKind
	volume       int
	chapterIndex int
	chapterRow   int
	focus        groupFocus
}

type batchRun interface {
	Events() <-chan cli.Event
	Pause()
	Resume()
	Cancel()
	Wait() cli.Summary
}

type backend interface {
	SetOutputRoot(string, bool) (string, error)
	DownloadedSeries(string) ([]string, error)
	PackNeedsRecovery(string) (bool, error)
	PackDownloaded(context.Context, string, bool, packer.Preset) (string, error)
	Search(context.Context, string) ([]komiku.Series, error)
	Discover(context.Context, string) ([]komiku.Chapter, error)
	LoadVolumes(context.Context, string, []komiku.Chapter) ([]komiku.Volume, error)
	LoadWikipediaVolumes(context.Context, string) ([]komiku.Volume, error)
	SaveManualVolumes(string, []komiku.Volume, int) error
	Start(context.Context, string, []cli.Job) (batchRun, error)
	RecordPackManifest(string, string, []komiku.Volume, []cli.Job, []cli.Result) error
	PreparePack(string, packer.Preset, []komiku.Volume, []cli.Job, []cli.Result) cli.PackPlan
	Pack(context.Context, cli.PackPlan) ([]cli.PackOutcome, error)
}

type model struct {
	backend    backend
	preset     packer.Preset
	outputRoot string
	plain      bool
	renderer   *lipgloss.Renderer
	now        func() time.Time

	screen screen
	width  int
	height int

	input         textinput.Model
	outputInput   textinput.Model
	packPathInput textinput.Model
	filter        textinput.Model
	rangeInput    textinput.Model
	spinner       spinner.Model
	progress      progress.Model

	homeCursor            int
	outputCursor          int
	outputPersist         bool
	packSeries            []string
	packSeriesCursor      int
	packSeriesDir         string
	standalonePackOutput  string
	standalonePackRecover bool
	loading               bool
	loadingLabel          string
	emptyLabel            string
	err                   error
	message               string
	opCancel              context.CancelFunc
	operationID           uint64
	searchResults         []komiku.Series
	searchCursor          int
	seriesURL             string
	chapters              []komiku.Chapter
	chapterCursor         int
	filtering             bool
	selected              map[string]bool
	flat                  bool
	assignments           map[string]int
	mappings              []komiku.Volume
	explicitFlat          bool
	mappingProvenance     string
	packUnavailableReason string
	groupView             bool
	groupLoading          bool
	groupLoaded           bool
	groupEmpty            bool
	groupErr              error
	groupMappings         []komiku.Volume
	groupFocus            groupFocus

	rangeMode    rangeMode
	rangeLoading bool

	batch            batchRun
	jobs             []cli.Job
	summary          cli.Summary
	completed        int
	latest           komiku.Chapter
	latestPage       int
	latestPages      int
	bytes            int64
	errorCount       int
	seenResultErrors map[string]struct{}
	startedAt        time.Time
	pausedAt         time.Time
	pausedFor        time.Duration
	paused           bool
	resuming         bool
	stopping         bool
	tail             []string

	packPlan     cli.PackPlan
	packCancel   context.CancelFunc
	packOutcomes []cli.PackOutcome
	packErr      error
}

func newModel(service backend, preset packer.Preset, plain bool, renderer *lipgloss.Renderer, now func() time.Time) model {
	if now == nil {
		now = time.Now
	}
	input := textinput.New()
	input.Prompt = "> "
	input.Placeholder = "Search title or paste series URL"
	input.CharLimit = 512
	input.Width = 60
	input.PlaceholderStyle = lipgloss.NewStyle()
	input.CompletionStyle = lipgloss.NewStyle()
	input.Focus()
	outputInput := textinput.New()
	outputInput.Prompt = "> "
	outputInput.Placeholder = "/path/to/manga"
	outputInput.CharLimit = 1024
	outputInput.Width = 60
	outputInput.PlaceholderStyle = lipgloss.NewStyle()
	outputInput.CompletionStyle = lipgloss.NewStyle()
	packPathInput := textinput.New()
	packPathInput.Prompt = "> "
	packPathInput.Placeholder = "/path/to/downloaded-manga"
	packPathInput.CharLimit = 1024
	packPathInput.Width = 60
	packPathInput.PlaceholderStyle = lipgloss.NewStyle()
	packPathInput.CompletionStyle = lipgloss.NewStyle()
	filter := textinput.New()
	filter.Prompt = "/ "
	filter.Placeholder = "Filter chapters"
	filter.CharLimit = 80
	filter.PlaceholderStyle = lipgloss.NewStyle()
	filter.CompletionStyle = lipgloss.NewStyle()
	rangeInput := textinput.New()
	rangeInput.Prompt = "> "
	rangeInput.CharLimit = 256
	rangeInput.PlaceholderStyle = lipgloss.NewStyle()
	rangeInput.CompletionStyle = lipgloss.NewStyle()
	spin := spinner.New()
	spin.Spinner = spinner.Line
	bar := progress.New(progress.WithSolidFill(""))
	bar.ShowPercentage = true
	bar.FullColor = ""
	bar.EmptyColor = ""
	bar.PercentageStyle = lipgloss.NewStyle()
	if plain {
		bar.Full = '#'
		bar.Empty = '-'
	}
	return model{
		backend: service, preset: preset, plain: plain, renderer: renderer, now: now,
		screen: searchScreen, width: 80, height: 24,
		input: input, outputInput: outputInput, packPathInput: packPathInput, filter: filter, rangeInput: rangeInput, spinner: spin, progress: bar,
		selected: make(map[string]bool), flat: true, assignments: make(map[string]int),
		seenResultErrors: make(map[string]struct{}),
	}
}

func (m *model) beginHome(outputRoot string) {
	m.outputRoot = outputRoot
	m.homeCursor = 0
	m.input.Blur()
	m.outputInput.Blur()
	m.err = nil
	m.message = ""
	m.screen = homeScreen
}

func (m *model) beginOutputSetup(outputRoot string) {
	m.outputRoot = outputRoot
	m.outputCursor = 0
	m.outputPersist = false
	m.outputInput.SetValue(outputRoot)
	m.outputInput.Focus()
	m.input.Blur()
	m.err = nil
	m.screen = outputScreen
}

func (m model) Init() tea.Cmd {
	if m.plain {
		return textinput.Blink
	}
	return tea.Batch(textinput.Blink, m.spinner.Tick)
}

type searchMsg struct {
	id      uint64
	results []komiku.Series
	err     error
}

type discoverMsg struct {
	id        uint64
	seriesURL string
	chapters  []komiku.Chapter
	err       error
}

type volumeSelectionMsg struct {
	id         uint64
	volumes    []komiku.Volume
	jobs       []cli.Job
	provenance string
	err        error
}
type wikipediaVolumesMsg struct {
	id      uint64
	volumes []komiku.Volume
	err     error
}

type downloadedSeriesMsg struct {
	series []string
	err    error
}
type packInspectMsg struct {
	seriesDir string
	recover   bool
	err       error
}
type standalonePackMsg struct {
	output string
	err    error
}

type engineEventMsg struct{ event cli.Event }
type batchClosedMsg struct{ summary cli.Summary }
type packMsg struct {
	outcomes []cli.PackOutcome
	err      error
}
type shutdownMsg struct{}

func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	var commands []tea.Cmd
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(msg.Width, 20), max(msg.Height, 8)
		m.input.Width = max(m.width-6, 10)
		m.outputInput.Width = max(m.width-6, 10)
		m.packPathInput.Width = max(m.width-6, 10)
		m.filter.Width = max(m.width-6, 10)
		m.rangeInput.Width = max(m.width-6, 10)
		m.progress.Width = max(min(m.width-2, 72), 12)
		return m, nil
	case shutdownMsg:
		return m.shutdown()
	case spinner.TickMsg:
		if (m.loading || m.rangeLoading || m.groupLoading || (m.screen == packingScreen || m.screen == standalonePackingScreen)) && !m.plain {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			commands = append(commands, cmd)
		}
	case searchMsg:
		if msg.id != m.operationID {
			return m, nil
		}
		m.opCancel = nil
		m.loading = false
		m.err = msg.err
		m.searchResults = msg.results
		m.searchCursor = 0
		if msg.err != nil {
			m.message = "Press Enter to retry or edit the query."
			m.input.Focus()
		} else if len(msg.results) == 0 {
			m.emptyLabel = "No series found. Edit the query and retry."
			m.input.Focus()
		} else {
			m.emptyLabel = ""
			m.input.Blur()
		}
		return m, nil
	case discoverMsg:
		if msg.id != m.operationID {
			return m, nil
		}
		m.opCancel = nil
		m.loading = false
		m.err = msg.err
		if msg.err != nil {
			m.message = "Press Enter to retry or edit the URL."
			m.input.Focus()
			return m, nil
		}
		if len(msg.chapters) == 0 {
			m.emptyLabel = "No chapters found at this series URL."
			m.input.Focus()
			return m, nil
		}
		m.seriesURL = msg.seriesURL
		m.chapters = msg.chapters
		m.screen = chaptersScreen
		m.selected = make(map[string]bool)
		m.assignments = make(map[string]int)
		m.mappings = nil
		m.flat = true
		m.explicitFlat = false
		m.mappingProvenance, m.packUnavailableReason = "", ""
		m.chapterCursor = 0
		m.err, m.message, m.emptyLabel = nil, "", ""
		m.groupView, m.groupLoading, m.groupLoaded, m.groupEmpty = false, false, false, false
		m.groupErr, m.groupMappings, m.groupFocus = nil, nil, groupFocus{}
		return m, nil
	case wikipediaVolumesMsg:
		if msg.id != m.operationID {
			return m, nil
		}
		m.opCancel = nil
		m.groupLoading = false
		if msg.err != nil {
			m.groupView = false
			m.groupErr = msg.err
			return m, nil
		}
		if len(msg.volumes) == 0 {
			m.groupView = false
			m.groupEmpty = true
			return m, nil
		}
		m.groupMappings = append([]komiku.Volume(nil), msg.volumes...)
		m.groupLoaded = true
		m.groupView = true
		m.focusGroupedChapter(m.chapterCursor)
		return m, nil
	case volumeSelectionMsg:
		if msg.id != m.operationID {
			return m, nil
		}
		m.opCancel = nil
		m.rangeLoading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.applyVolumeJobs(msg.jobs, msg.volumes)
		m.mappingProvenance = msg.provenance
		m.screen = chaptersScreen
		m.message = fmt.Sprintf("Selected %d chapters from %d volume(s).", len(msg.jobs), len(msg.volumes))
		return m, nil
	case downloadedSeriesMsg:
		m.loading = false
		m.err = msg.err
		if msg.err == nil {
			m.packSeries = msg.series
			m.packSeriesCursor = 0
			m.screen = packSeriesScreen
		}
		return m, nil
	case packInspectMsg:
		m.loading = false
		m.err = msg.err
		if msg.err != nil {
			return m, nil
		}
		m.packSeriesDir = msg.seriesDir
		m.standalonePackRecover = msg.recover
		if msg.recover {
			m.screen = packRecoveryScreen
			return m, nil
		}
		return m.startStandalonePack(false)
	case standalonePackMsg:
		m.packCancel = nil
		m.standalonePackOutput = msg.output
		m.err = msg.err
		if m.stopping {
			return m, tea.Quit
		}
		m.screen = standalonePackDoneScreen
		return m, nil
	case engineEventMsg:
		m.applyEngineEvent(msg.event)
		return m, waitBatchEvent(m.batch)
	case batchClosedMsg:
		m.batch = nil
		m.syncResultErrors(msg.summary.Results)
		var manifestErr error
		if len(m.mappings) > 0 && m.mappingProvenance != "" {
			manifestErr = m.backend.RecordPackManifest(m.seriesURL, m.mappingProvenance, m.mappings, m.jobs, msg.summary.Results)
			if manifestErr != nil {
				msg.summary.Err = errors.Join(msg.summary.Err, fmt.Errorf("save pack manifest: %w", manifestErr))
			}
		}
		m.summary = msg.summary
		m.packPlan = m.backend.PreparePack(m.seriesURL, m.preset, m.mappings, m.jobs, msg.summary.Results)
		if manifestErr != nil {
			m.packPlan = cli.PackPlan{Preset: m.preset, DisabledReason: "Pack metadata could not be saved: " + manifestErr.Error()}
		} else if m.packPlan.DisabledReason != "" && m.packUnavailableReason != "" {
			m.packPlan.DisabledReason = m.packUnavailableReason
		}
		m.err = msg.summary.Err
		if m.stopping {
			return m, tea.Quit
		}
		m.screen = doneScreen
		if msg.summary.Err != nil {
			m.message = "Download data finished, but finalization failed."
		} else {
			m.message = "Download finished."
		}
		return m, nil
	case packMsg:
		m.packOutcomes, m.packErr = msg.outcomes, msg.err
		m.packCancel = nil
		if m.stopping {
			return m, tea.Quit
		}
		m.screen = doneScreen
		switch {
		case msg.err != nil:
			m.message = "Pack stopped before every volume finished."
		case packOutcomesFailed(msg.outcomes):
			m.message = "Pack finished with volume failures."
		default:
			m.message = "Pack finished."
		}
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m.shutdown()
		}
		switch m.screen {
		case homeScreen:
			return m.updateHome(msg)
		case outputScreen:
			return m.updateOutput(msg)
		case searchScreen:
			return m.updateSearch(msg)
		case chaptersScreen:
			return m.updateChapters(msg)
		case rangeScreen:
			return m.updateRange(msg)
		case downloadingScreen:
			return m.updateDownloading(msg)
		case doneScreen:
			return m.updateDone(msg)
		case packingScreen:
			return m.updatePacking(msg)
		case packSeriesScreen:
			return m.updatePackSeries(msg)
		case packPathScreen:
			return m.updatePackPath(msg)
		case packRecoveryScreen:
			return m.updatePackRecovery(msg)
		case standalonePackingScreen:
			return m.updatePacking(msg)
		case standalonePackDoneScreen:
			return m.updateStandalonePackDone(msg)
		}
	}
	return m, tea.Batch(commands...)
}

func (m model) shutdown() (tea.Model, tea.Cmd) {
	m.invalidateOperation()
	if m.batch != nil {
		m.stopping = true
		m.message = "Stopping after active requests close..."
		m.batch.Cancel()
		return m, nil
	}
	if m.packCancel != nil {
		m.stopping = true
		m.message = "Stopping after the current pack page..."
		m.packCancel()
		return m, nil
	}
	return m, tea.Quit
}

func (m *model) beginOperation() (context.Context, uint64) {
	m.invalidateOperation()
	ctx, cancel := context.WithCancel(context.Background())
	m.opCancel = cancel
	return ctx, m.operationID
}

func (m *model) invalidateOperation() {
	if m.opCancel != nil {
		m.opCancel()
		m.opCancel = nil
	}
	m.operationID++
}

func (m model) withSpinner(cmd tea.Cmd) tea.Cmd {
	if m.plain {
		return cmd
	}
	return tea.Batch(cmd, m.spinner.Tick)
}

func (m model) updateHome(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "up", "k", "down", "j":
		m.homeCursor = (m.homeCursor + 1) % 2
	case "enter":
		if m.homeCursor == 0 {
			m.beginOutputSetup(m.outputRoot)
			return m, textinput.Blink
		}
		m.loading = true
		m.err = nil
		return m, func() tea.Msg {
			series, err := m.backend.DownloadedSeries(m.outputRoot)
			return downloadedSeriesMsg{series: series, err: err}
		}
	case "q", "esc":
		return m, tea.Quit
	}
	return m, nil
}

func (m model) updatePackSeries(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.loading {
		return m, nil
	}
	switch key.String() {
	case "up", "k":
		m.packSeriesCursor = max(m.packSeriesCursor-1, 0)
	case "down", "j":
		m.packSeriesCursor = min(m.packSeriesCursor+1, len(m.packSeries))
	case "enter":
		if m.packSeriesCursor == len(m.packSeries) {
			m.packPathInput.SetValue("")
			m.packPathInput.Focus()
			m.err = nil
			m.screen = packPathScreen
			return m, textinput.Blink
		}
		dir := m.packSeries[m.packSeriesCursor]
		m.loading = true
		m.err = nil
		return m, func() tea.Msg {
			recover, err := m.backend.PackNeedsRecovery(dir)
			return packInspectMsg{seriesDir: dir, recover: recover, err: err}
		}
	case "esc":
		m.beginHome(m.outputRoot)
	}
	return m, nil
}

func (m model) updatePackPath(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.packPathInput.Blur()
		m.screen = packSeriesScreen
		return m, nil
	case "enter":
		dir := strings.TrimSpace(m.packPathInput.Value())
		if dir == "" {
			m.err = errors.New("enter a downloaded manga folder")
			return m, nil
		}
		m.packPathInput.Blur()
		m.loading = true
		m.err = nil
		return m, func() tea.Msg {
			recover, err := m.backend.PackNeedsRecovery(dir)
			return packInspectMsg{seriesDir: dir, recover: recover, err: err}
		}
	}
	var cmd tea.Cmd
	m.packPathInput, cmd = m.packPathInput.Update(key)
	m.err = nil
	return m, cmd
}

func (m model) updatePackRecovery(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "enter":
		return m.startStandalonePack(true)
	case "esc":
		m.screen = packSeriesScreen
	case "q":
		return m, tea.Quit
	}
	return m, nil
}

func (m model) startStandalonePack(recover bool) (tea.Model, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	m.packCancel = cancel
	m.standalonePackRecover = recover
	m.stopping = false
	m.screen = standalonePackingScreen
	m.err = nil
	dir := m.packSeriesDir
	cmd := func() tea.Msg {
		output, err := m.backend.PackDownloaded(ctx, dir, recover, m.preset)
		return standalonePackMsg{output: output, err: err}
	}
	return m, m.withSpinner(cmd)
}

func (m model) updateStandalonePackDone(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "enter", "esc":
		m.beginHome(m.outputRoot)
	case "q":
		return m, tea.Quit
	}
	return m, nil
}

func (m model) updateOutput(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		return m, tea.Quit
	case "tab", "down":
		m.outputCursor = (m.outputCursor + 1) % 2
	case "shift+tab", "up":
		m.outputCursor = (m.outputCursor + 1) % 2
	case " ":
		if m.outputCursor == 1 {
			m.outputPersist = !m.outputPersist
			m.err = nil
			return m, nil
		}
	case "enter":
		path := strings.TrimSpace(m.outputInput.Value())
		if path == "" {
			m.err = errors.New("enter a download folder")
			return m, nil
		}
		resolved, err := m.backend.SetOutputRoot(path, m.outputPersist)
		if err != nil {
			m.err = err
			return m, nil
		}
		m.outputRoot = resolved
		m.outputInput.Blur()
		m.input.Focus()
		m.err = nil
		m.screen = searchScreen
		return m, nil
	}
	if m.outputCursor == 0 {
		m.outputInput.Focus()
		var cmd tea.Cmd
		m.outputInput, cmd = m.outputInput.Update(key)
		m.err = nil
		return m, cmd
	}
	m.outputInput.Blur()
	return m, nil
}

func (m model) updateSearch(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.loading {
		if key.String() == "esc" {
			m.invalidateOperation()
			m.loading = false
			m.input.Focus()
		}
		return m, nil
	}
	if len(m.searchResults) > 0 && !m.input.Focused() {
		switch key.String() {
		case "up", "k":
			m.searchCursor = max(m.searchCursor-1, 0)
		case "down", "j":
			m.searchCursor = min(m.searchCursor+1, len(m.searchResults)-1)
		case "enter":
			return m.startDiscover(m.searchResults[m.searchCursor].URL)
		case "esc":
			m.searchResults = nil
			m.input.Focus()
		case "q":
			return m, tea.Quit
		}
		return m, nil
	}
	if key.String() == "enter" {
		query := strings.TrimSpace(m.input.Value())
		if query == "" {
			m.err = fmt.Errorf("enter a title or series URL")
			return m, nil
		}
		if isHTTPURL(query) {
			return m.startDiscover(query)
		}
		return m.startSearch(query)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(key)
	m.err, m.emptyLabel, m.message = nil, "", ""
	return m, cmd
}

func (m model) startSearch(query string) (tea.Model, tea.Cmd) {
	ctx, id := m.beginOperation()
	m.loading, m.loadingLabel = true, "Searching series"
	m.err, m.emptyLabel, m.message, m.searchResults = nil, "", "", nil
	m.input.Blur()
	cmd := func() tea.Msg {
		results, err := m.backend.Search(ctx, query)
		return searchMsg{id: id, results: results, err: err}
	}
	return m, m.withSpinner(cmd)
}

func (m model) startDiscover(seriesURL string) (tea.Model, tea.Cmd) {
	ctx, id := m.beginOperation()
	m.loading, m.loadingLabel = true, "Loading chapters"
	m.err, m.emptyLabel, m.message = nil, "", ""
	m.input.Blur()
	cmd := func() tea.Msg {
		chapters, err := m.backend.Discover(ctx, seriesURL)
		return discoverMsg{id: id, seriesURL: seriesURL, chapters: chapters, err: err}
	}
	return m, m.withSpinner(cmd)
}

func (m model) updateChapters(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.filtering {
		switch key.String() {
		case "enter":
			m.filtering = false
			m.filter.Blur()
			m.chapterCursor = 0
			m.focusGroupedChapter(0)
			return m, nil
		case "esc":
			m.filtering = false
			m.filter.SetValue("")
			m.filter.Blur()
			m.chapterCursor = 0
			m.focusGroupedChapter(0)
			return m, nil
		}
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(key)
		m.chapterCursor = 0
		m.focusGroupedChapter(0)
		return m, cmd
	}
	visible := m.visibleChapterIndices()
	grouped := m.groupView && m.groupLoaded
	var groupedRows []groupedRow
	if grouped {
		groupedRows = m.normalizeGroupedFocus(visible)
	} else if len(visible) == 0 {
		m.chapterCursor = 0
	} else if m.chapterCursor >= len(visible) {
		m.chapterCursor = len(visible) - 1
	}
	switch key.String() {
	case "up", "k":
		if grouped {
			m.moveGroupedFocus(groupedRows, -1)
		} else {
			m.chapterCursor = max(m.chapterCursor-1, 0)
		}
	case "down", "j":
		if grouped {
			m.moveGroupedFocus(groupedRows, 1)
		} else if len(visible) > 0 {
			m.chapterCursor = min(m.chapterCursor+1, len(visible)-1)
		}
	case " ":
		if grouped {
			if row, ok := m.focusedGroupedRow(groupedRows); ok {
				if row.kind == groupedVolumeRow {
					m.toggleWikipediaVolumeSelection(row.volume)
					return m, nil
				}
				if row.kind == groupedChapterRow {
					m.chapterCursor = row.chapterRow
				}
			}
		}
		if len(visible) > 0 {
			chapter := m.chapters[visible[m.chapterCursor]]
			if !m.flat && m.assignments[chapter.URL] == 0 {
				m.message = fmt.Sprintf("Chapter %s is outside the selected volume mappings; use r then flat to select it.", chapter.Display)
				return m, nil
			}
			m.selected[chapter.URL] = !m.selected[chapter.URL]
			if !m.flat {
				m.message = "Mapped selection changed; incomplete volumes will be reported as not packable."
			}
		}
	case "a":
		eligible := make([]int, 0, len(visible))
		for _, index := range visible {
			if m.flat || m.assignments[m.chapters[index].URL] > 0 {
				eligible = append(eligible, index)
			}
		}
		if len(eligible) == 0 {
			if !m.flat {
				m.message = "No visible chapters belong to the selected volume mappings; use r then flat to change layout."
			}
			return m, nil
		}
		allSelected := true
		for _, index := range eligible {
			allSelected = allSelected && m.selected[m.chapters[index].URL]
		}
		for _, index := range eligible {
			m.selected[m.chapters[index].URL] = !allSelected
		}
		if !m.flat {
			m.message = "Mapped visible-all changed only chapters covered by selected volumes."
		}
	case "/":
		m.filtering = true
		m.filter.Focus()
		return m, textinput.Blink
	case "v":
		return m.toggleWikipediaVolumes()
	case "r":
		m.cancelWikipediaVolumeLoad()
		m.screen = rangeScreen
		m.rangeMode = chapterRange
		m.rangeInput.SetValue("")
		m.rangeInput.Focus()
		m.err = nil
		return m, textinput.Blink
	case "enter":
		m.cancelWikipediaVolumeLoad()
		return m.startDownload()
	case "q":
		return m, tea.Quit
	}
	return m, nil
}

func (m model) toggleWikipediaVolumes() (tea.Model, tea.Cmd) {
	if m.groupView {
		if m.groupLoaded {
			m.handoffGroupedFocus()
		}
		if m.groupLoading {
			m.invalidateOperation()
		}
		m.groupView = false
		m.groupLoading = false
		m.groupErr = nil
		m.groupEmpty = false
		return m, nil
	}
	m.groupErr = nil
	m.groupEmpty = false
	if m.groupLoaded {
		m.groupView = true
		m.focusGroupedChapter(m.chapterCursor)
		return m, nil
	}
	ctx, id := m.beginOperation()
	m.groupView = true
	m.groupLoading = true
	cmd := func() tea.Msg {
		volumes, err := m.backend.LoadWikipediaVolumes(ctx, m.seriesURL)
		return wikipediaVolumesMsg{id: id, volumes: volumes, err: err}
	}
	return m, m.withSpinner(cmd)
}

func (m model) updateRange(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.rangeLoading {
		if key.String() == "esc" {
			m.invalidateOperation()
			m.rangeLoading = false
		}
		return m, nil
	}
	switch key.String() {
	case "esc":
		m.screen = chaptersScreen
		m.rangeInput.Blur()
		m.err = nil
		return m, nil
	case "tab", "right":
		m.rangeMode = (m.rangeMode + 1) % 4
		m.rangeInput.SetValue("")
		m.err = nil
		return m, nil
	case "shift+tab", "left":
		m.rangeMode = (m.rangeMode + 3) % 4
		m.rangeInput.SetValue("")
		m.err = nil
		return m, nil
	case "enter":
		return m.applyRange()
	}
	var cmd tea.Cmd
	m.rangeInput, cmd = m.rangeInput.Update(key)
	m.err = nil
	return m, cmd
}

func (m model) applyRange() (tea.Model, tea.Cmd) {
	expression := strings.TrimSpace(m.rangeInput.Value())
	switch m.rangeMode {
	case chapterRange:
		selected, err := selectChapterExpression(m.chapters, expression)
		if err != nil {
			m.err = err
			return m, nil
		}
		if !m.flat {
			for _, chapter := range selected {
				if m.assignments[chapter.URL] == 0 {
					m.err = fmt.Errorf("chapter %s is outside selected volume mappings; use flat mode explicitly", chapter.Display)
					return m, nil
				}
			}
		}
		m.selected = make(map[string]bool, len(selected))
		for _, chapter := range selected {
			m.selected[chapter.URL] = true
		}
		m.screen = chaptersScreen
		if m.flat {
			m.message = fmt.Sprintf("Selected %d discovered chapter(s) in flat layout.", len(selected))
		} else {
			m.message = fmt.Sprintf("Selected %d discovered chapter(s) within mapped volume coverage.", len(selected))
		}
		return m, nil
	case flatRange:
		selected, err := selectChapterExpression(m.chapters, expression)
		if err != nil {
			m.err = err
			return m, nil
		}
		m.selected = make(map[string]bool, len(selected))
		for _, chapter := range selected {
			m.selected[chapter.URL] = true
		}
		m.explicitFlat = true
		m.mappingProvenance = ""
		m.resetToFlatSelection()
		m.screen = chaptersScreen
		m.message = fmt.Sprintf("Selected %d discovered chapter(s) in flat layout.", len(selected))
		return m, nil
	case volumeRange:
		if expression == "" {
			m.err = fmt.Errorf("enter a volume list or range")
			return m, nil
		}
		ctx, id := m.beginOperation()
		m.rangeLoading = true
		cmd := func() tea.Msg {
			volumes, err := m.backend.LoadVolumes(ctx, m.seriesURL, m.chapters)
			if err != nil {
				return volumeSelectionMsg{id: id, err: err}
			}
			jobs, err := cli.SelectVolumes(m.chapters, volumes, expression)
			if err != nil {
				return volumeSelectionMsg{id: id, err: err}
			}
			selectedMappings, err := cli.SelectedVolumeMappings(volumes, expression)
			return volumeSelectionMsg{id: id, volumes: selectedMappings, jobs: jobs, provenance: "download-mapping", err: err}
		}
		return m, m.withSpinner(cmd)
	case manualRange:
		parts := strings.Split(expression, "|")
		if len(parts) != 2 {
			m.err = fmt.Errorf("use mapping | volumes, for example 1:1-7,2:8-15 | 1-2")
			return m, nil
		}
		volumes, err := cli.ParseManualVolumeMapping(strings.TrimSpace(parts[0]), komiku.MaxDiscoveredChapter(m.chapters))
		if err != nil {
			m.err = err
			return m, nil
		}
		selection := strings.TrimSpace(parts[1])
		jobs, err := cli.SelectVolumes(m.chapters, volumes, selection)
		if err != nil {
			m.err = err
			return m, nil
		}
		selectedMappings, err := cli.SelectedVolumeMappings(volumes, selection)
		if err != nil {
			m.err = err
			return m, nil
		}
		maxChapter := komiku.MaxDiscoveredChapter(m.chapters)
		ctx, id := m.beginOperation()
		m.rangeLoading = true
		cmd := func() tea.Msg {
			if err := ctx.Err(); err != nil {
				return volumeSelectionMsg{id: id, err: err}
			}
			if err := m.backend.SaveManualVolumes(m.seriesURL, volumes, maxChapter); err != nil {
				return volumeSelectionMsg{id: id, err: err}
			}
			return volumeSelectionMsg{id: id, volumes: selectedMappings, jobs: jobs, provenance: "manual-range"}
		}
		return m, m.withSpinner(cmd)
	}
	return m, nil
}

func selectChapterExpression(chapters []komiku.Chapter, expression string) ([]komiku.Chapter, error) {
	if strings.EqualFold(expression, "all") {
		return append([]komiku.Chapter(nil), chapters...), nil
	}
	return cli.SelectChapters(chapters, expression)
}

func (m *model) applyVolumeJobs(jobs []cli.Job, volumes []komiku.Volume) {
	m.selected = make(map[string]bool, len(jobs))
	m.assignments = make(map[string]int, len(jobs))
	for _, job := range jobs {
		m.selected[job.Chapter.URL] = true
		m.assignments[job.Chapter.URL] = job.Volume
	}
	m.mappings = append([]komiku.Volume(nil), volumes...)
	m.flat = false
	m.explicitFlat = false
	m.packUnavailableReason = ""
}

func (m *model) resetToFlatSelection() {
	m.flat = true
	m.assignments = make(map[string]int)
	m.mappings = nil
}

func (m model) startDownload() (tea.Model, tea.Cmd) {
	if m.flat {
		switch {
		case m.explicitFlat:
			m.packUnavailableReason = "Pack unavailable: explicit flat layout was selected with r."
		case m.groupLoaded:
			resolved, err := cli.ResolveCompleteVolumeSelection(m.chapters, m.selected, m.groupMappings)
			if err != nil {
				m.packUnavailableReason = "Pack unavailable: selection is not complete Wikipedia volumes: " + err.Error() + "."
			} else {
				m.applyVolumeJobs(resolved.Jobs, resolved.Mappings)
				m.mappingProvenance = "wikipedia-display"
				m.message = fmt.Sprintf("Promoted %d complete Wikipedia volume(s) to mapped download layout.", len(resolved.Mappings))
			}
		default:
			m.packUnavailableReason = "Pack unavailable: select complete Wikipedia volumes with v, or choose a mapped layout with r."
		}
	}
	jobs := m.selectedJobs()
	if len(jobs) == 0 {
		m.message = "Select at least one chapter before starting."
		return m, nil
	}
	batch, err := m.backend.Start(context.Background(), m.seriesURL, jobs)
	if err != nil {
		m.err = err
		return m, nil
	}
	m.jobs = jobs
	m.batch = batch
	m.screen = downloadingScreen
	m.summary = cli.Summary{}
	m.completed, m.latestPage, m.latestPages, m.errorCount = 0, 0, 0, 0
	m.seenResultErrors = make(map[string]struct{})
	m.bytes, m.pausedFor = 0, 0
	m.startedAt, m.pausedAt = time.Time{}, time.Time{}
	m.paused, m.resuming, m.stopping = false, false, false
	m.tail = nil
	m.err = nil
	if m.message == "" {
		m.message = "Downloading"
	}
	return m, waitBatchEvent(batch)
}

func (m model) selectedJobs() []cli.Job {
	ambiguous := make(map[float64]bool)
	first := make(map[float64]string)
	for _, chapter := range m.chapters {
		if raw, ok := first[chapter.Number]; ok && raw != chapter.RawID {
			ambiguous[chapter.Number] = true
		} else {
			first[chapter.Number] = chapter.RawID
		}
	}
	jobs := make([]cli.Job, 0, len(m.selected))
	for _, chapter := range m.chapters {
		if !m.selected[chapter.URL] {
			continue
		}
		volume := m.assignments[chapter.URL]
		jobs = append(jobs, cli.Job{Chapter: chapter, Volume: volume, Flat: m.flat || volume == 0, Ambiguous: ambiguous[chapter.Number]})
	}
	return jobs
}

func waitBatchEvent(batch batchRun) tea.Cmd {
	return func() tea.Msg {
		event, open := <-batch.Events()
		if !open {
			return batchClosedMsg{summary: batch.Wait()}
		}
		return engineEventMsg{event: event}
	}
}

func (m *model) applyEngineEvent(event cli.Event) {
	switch event.Kind {
	case cli.BatchStarted:
		m.startedAt = event.At
	case cli.ChapterStarted:
		m.latest = event.Chapter
		m.latestPage, m.latestPages = 0, 0
	case cli.ChapterPagesKnown:
		m.latest, m.latestPages = event.Chapter, event.Pages
	case cli.PageStarted, cli.PageSkipped:
		m.latest, m.latestPage, m.latestPages = event.Chapter, event.Page, event.Pages
	case cli.PageDone:
		m.latest, m.latestPage, m.latestPages = event.Chapter, event.Page, event.Pages
		m.bytes += event.Bytes
	case cli.PageFailed:
		m.latest, m.latestPage, m.latestPages = event.Chapter, event.Page, event.Pages
		m.addResultError(event.Chapter, fmt.Sprintf("page %03d: %v", event.Page, event.Err))
		m.appendTail(fmt.Sprintf("[FAIL] ch %s page %d: %v", event.Chapter.Display, event.Page, event.Err))
	case cli.ChapterFinished:
		m.completed++
		resultChapter := event.Result.Chapter
		if resultChapter.URL == "" && resultChapter.RawID == "" {
			resultChapter = event.Chapter
		}
		for _, message := range event.Result.Errors {
			m.addResultError(resultChapter, message)
		}
		m.appendTail(fmt.Sprintf("[%s] ch %s pages=%d/%d", event.Result.Status, event.Chapter.Display, event.Result.Success, event.Result.Total))
	case cli.BatchPaused:
		m.paused, m.resuming = true, false
		m.pausedAt = event.At
		m.message = "Paused. Space resumes; q stops safely."
	case cli.BatchResumed:
		if !m.pausedAt.IsZero() {
			m.pausedFor += event.At.Sub(m.pausedAt)
		}
		m.paused, m.resuming = false, false
		m.message = "Downloading"
	case cli.BatchStopping:
		m.stopping = true
		m.message = "Stopping after active requests close..."
	case cli.BatchFinished:
		m.message = "Download and audit finished."
	}
}

func (m *model) addResultError(chapter komiku.Chapter, message string) {
	if message == "" {
		return
	}
	identity := chapter.URL
	if identity == "" {
		identity = chapter.RawID
	}
	key := identity + "\x00" + message
	if _, exists := m.seenResultErrors[key]; exists {
		return
	}
	m.seenResultErrors[key] = struct{}{}
	m.errorCount++
}

func (m *model) syncResultErrors(results []cli.Result) {
	m.seenResultErrors = make(map[string]struct{})
	m.errorCount = 0
	for _, result := range results {
		for _, message := range result.Errors {
			m.addResultError(result.Chapter, message)
		}
	}
}

func (m *model) appendTail(line string) {
	m.tail = append(m.tail, line)
	if len(m.tail) > 6 {
		m.tail = append([]string(nil), m.tail[len(m.tail)-6:]...)
	}
}

func (m model) updateDownloading(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case " ":
		if m.stopping {
			return m, nil
		}
		if m.paused {
			m.resuming = true
			m.message = "Resuming with global pacing..."
			m.batch.Resume()
		} else {
			m.paused = true
			m.message = "Pausing after active requests..."
			m.batch.Pause()
		}
	case "q":
		if !m.stopping {
			m.stopping = true
			m.message = "Stopping after active requests close..."
			m.batch.Cancel()
		}
	}
	return m, nil
}

func (m model) updateDone(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "q":
		return m, tea.Quit
	case "p":
		if m.packPlan.DisabledReason != "" || len(m.packPlan.Volumes) == 0 {
			m.message = m.packPlan.DisabledReason
			return m, nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		m.packCancel = cancel
		m.screen = packingScreen
		m.stopping = false
		m.message = fmt.Sprintf("Packing %d volume(s) with preset %s...", len(m.packPlan.Volumes), m.packPlan.Preset)
		plan := m.packPlan
		cmd := func() tea.Msg {
			outcomes, err := m.backend.Pack(ctx, plan)
			return packMsg{outcomes: outcomes, err: err}
		}
		return m, m.withSpinner(cmd)
	}
	return m, nil
}

func (m model) updatePacking(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "q" && !m.stopping {
		m.stopping = true
		m.message = "Stopping after the current pack page..."
		m.packCancel()
	}
	return m, nil
}

func exactWikipediaVolumeForChapter(number float64, mappings []komiku.Volume) (int, bool) {
	if number <= 0 || number != math.Trunc(number) {
		return 0, false
	}
	for _, mapping := range mappings {
		if number >= float64(mapping.Start) && number <= float64(mapping.End) {
			return mapping.Volume, true
		}
	}
	return 0, false
}

func (m model) groupedRows(visible []int) []groupedRow {
	rows := make([]groupedRow, 0, len(visible)+len(m.groupMappings)+1)
	lastGroup := -1
	for chapterRow, chapterIndex := range visible {
		chapter := m.chapters[chapterIndex]
		volume, mapped := exactWikipediaVolumeForChapter(chapter.Number, m.groupMappings)
		if !mapped {
			volume = 0
		}
		if volume != lastGroup {
			if mapped {
				rows = append(rows, groupedRow{
					kind:   groupedVolumeRow,
					volume: volume,
					focus:  groupFocus{kind: groupFocusVolume, volume: volume},
				})
			} else {
				rows = append(rows, groupedRow{kind: groupedUnmappedRow})
			}
			lastGroup = volume
		}
		rows = append(rows, groupedRow{
			kind:         groupedChapterRow,
			volume:       volume,
			chapterIndex: chapterIndex,
			chapterRow:   chapterRow,
			focus:        groupFocus{kind: groupFocusChapter, chapterURL: chapter.URL},
		})
	}
	return rows
}

func sameGroupFocus(left, right groupFocus) bool {
	if left.kind != right.kind {
		return false
	}
	switch left.kind {
	case groupFocusVolume:
		return left.volume == right.volume
	case groupFocusChapter:
		return left.chapterURL == right.chapterURL
	default:
		return false
	}
}

func groupedFocusIndex(rows []groupedRow, focus groupFocus) int {
	for index, row := range rows {
		if sameGroupFocus(row.focus, focus) {
			return index
		}
	}
	return -1
}

func (m *model) normalizeGroupedFocus(visible []int) []groupedRow {
	rows := m.groupedRows(visible)
	if groupedFocusIndex(rows, m.groupFocus) >= 0 {
		return rows
	}
	if len(visible) == 0 {
		m.groupFocus = groupFocus{}
		m.chapterCursor = 0
		return rows
	}
	m.chapterCursor = min(max(m.chapterCursor, 0), len(visible)-1)
	m.groupFocus = groupFocus{kind: groupFocusChapter, chapterURL: m.chapters[visible[m.chapterCursor]].URL}
	return rows
}

func (m *model) focusGroupedChapter(chapterRow int) {
	if !(m.groupView && m.groupLoaded) {
		return
	}
	visible := m.visibleChapterIndices()
	if len(visible) == 0 {
		m.groupFocus = groupFocus{}
		m.chapterCursor = 0
		return
	}
	m.chapterCursor = min(max(chapterRow, 0), len(visible)-1)
	m.groupFocus = groupFocus{kind: groupFocusChapter, chapterURL: m.chapters[visible[m.chapterCursor]].URL}
}

func (m *model) moveGroupedFocus(rows []groupedRow, delta int) {
	current := groupedFocusIndex(rows, m.groupFocus)
	if current < 0 {
		return
	}
	for index := current + delta; index >= 0 && index < len(rows); index += delta {
		if rows[index].focus.kind == groupFocusNone {
			continue
		}
		m.groupFocus = rows[index].focus
		if rows[index].kind == groupedChapterRow {
			m.chapterCursor = rows[index].chapterRow
		}
		return
	}
}

func (m model) focusedGroupedRow(rows []groupedRow) (groupedRow, bool) {
	index := groupedFocusIndex(rows, m.groupFocus)
	if index < 0 {
		return groupedRow{}, false
	}
	return rows[index], true
}

func (m *model) handoffGroupedFocus() {
	visible := m.visibleChapterIndices()
	rows := m.normalizeGroupedFocus(visible)
	focused, ok := m.focusedGroupedRow(rows)
	if !ok {
		return
	}
	if focused.kind == groupedChapterRow {
		m.chapterCursor = focused.chapterRow
		return
	}
	if focused.kind == groupedVolumeRow {
		for _, row := range rows {
			if row.kind == groupedChapterRow && row.volume == focused.volume {
				m.chapterCursor = row.chapterRow
				return
			}
		}
	}
}

func (m *model) cancelWikipediaVolumeLoad() {
	if !m.groupLoading {
		return
	}
	m.invalidateOperation()
	m.groupLoading = false
	m.groupView = false
}

func (m *model) toggleWikipediaVolumeSelection(volume int) {
	indices := make([]int, 0)
	for index, chapter := range m.chapters {
		if mappedVolume, mapped := exactWikipediaVolumeForChapter(chapter.Number, m.groupMappings); mapped && mappedVolume == volume {
			indices = append(indices, index)
		}
	}
	if len(indices) == 0 {
		return
	}
	allSelected := true
	for _, index := range indices {
		allSelected = allSelected && m.selected[m.chapters[index].URL]
	}
	if !allSelected && !m.flat {
		for _, index := range indices {
			if m.assignments[m.chapters[index].URL] == 0 {
				m.message = fmt.Sprintf("Volume %02d includes chapters outside the selected download mappings; use r then flat to change them.", volume)
				return
			}
		}
	}
	for _, index := range indices {
		m.selected[m.chapters[index].URL] = !allSelected
	}
	if !m.flat {
		m.message = "Mapped selection changed; incomplete volumes will be reported as not packable."
	}
}

func (m model) wikipediaVolumeSelectionMark(volume int) string {
	total, selected := 0, 0
	for _, chapter := range m.chapters {
		if mappedVolume, mapped := exactWikipediaVolumeForChapter(chapter.Number, m.groupMappings); mapped && mappedVolume == volume {
			total++
			if m.selected[chapter.URL] {
				selected++
			}
		}
	}
	switch {
	case selected == 0:
		return "[ ]"
	case selected == total:
		return "[x]"
	default:
		return "[-]"
	}
}

func (m model) visibleChapterIndices() []int {
	filter := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	indices := make([]int, 0, len(m.chapters))
	for index, chapter := range m.chapters {
		if filter == "" || strings.Contains(strings.ToLower(chapter.Display), filter) || strings.Contains(strings.ToLower(chapter.RawID), filter) {
			indices = append(indices, index)
		}
	}
	return indices
}

func (m model) selectedCount() int {
	count := 0
	for _, selected := range m.selected {
		if selected {
			count++
		}
	}
	return count
}

func isHTTPURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

func sortedPackOutcomes(outcomes []cli.PackOutcome) []cli.PackOutcome {
	result := append([]cli.PackOutcome(nil), outcomes...)
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Volume != result[j].Volume {
			return result[i].Volume < result[j].Volume
		}
		return result[i].Result.Path < result[j].Result.Path
	})
	return result
}

func packOutcomesFailed(outcomes []cli.PackOutcome) bool {
	for _, outcome := range outcomes {
		if outcome.Err != nil {
			return true
		}
	}
	return false
}
