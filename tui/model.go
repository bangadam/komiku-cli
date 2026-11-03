package tui

import (
	"context"
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
	searchScreen screen = iota
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
	downloadsScreen
	settingsScreen
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
	SetPreset(packer.Preset) error
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

type viewStyles struct {
	frame   lipgloss.Style
	heading lipgloss.Style
	focus   lipgloss.Style
}

type model struct {
	backend    backend
	preset     packer.Preset
	outputRoot string
	plain      bool
	styles     viewStyles
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

	nav                   int
	prevNav               int
	settingsCursor        int
	settingsPreset        packer.Preset
	packFromDownloads     bool
	downloads             []downloadStatus
	downloadsCursor       int
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
	input.PlaceholderStyle = lipgloss.NewStyle()
	input.CompletionStyle = lipgloss.NewStyle()
	input.Focus()
	outputInput := textinput.New()
	outputInput.Prompt = "> "
	outputInput.Placeholder = "/path/to/manga"
	outputInput.CharLimit = 1024
	outputInput.PlaceholderStyle = lipgloss.NewStyle()
	outputInput.CompletionStyle = lipgloss.NewStyle()
	packPathInput := textinput.New()
	packPathInput.Prompt = "> "
	packPathInput.Placeholder = "/path/to/downloaded-manga"
	packPathInput.CharLimit = 1024
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
		backend: service, preset: preset, plain: plain, styles: newViewStyles(plain, renderer), now: now,
		screen: searchScreen, width: 80, height: 24,
		input: input, outputInput: outputInput, packPathInput: packPathInput, filter: filter, rangeInput: rangeInput, spinner: spin, progress: bar,
		selected: make(map[string]bool), flat: true, assignments: make(map[string]int),
		seenResultErrors: make(map[string]struct{}),
	}
}

var navLabels = []string{"Search", "To CBZ", "Downloads", "Settings"}

var presetCycle = []packer.Preset{packer.Medium, packer.Small, packer.Tiny, packer.Raw}

func (m model) switchNav(index int) (tea.Model, tea.Cmd) {
	m.prevNav = m.nav
	m.nav = index
	m.err, m.message = nil, ""
	m.packFromDownloads = false
	m.input.Blur()
	m.outputInput.Blur()
	m.packPathInput.Blur()
	m.filter.Blur()
	m.rangeInput.Blur()
	switch index {
	case 0:
		m.screen = searchScreen
		m.input.Focus()
		return m, textinput.Blink
	case 1:
		m.screen = packSeriesScreen
		m.packSeriesCursor = 0
		return m, m.rescanPackList()
	case 2:
		m.screen = downloadsScreen
		m.downloadsCursor = 0
		return m, m.rescanDownloads()
	default:
		m.beginSettings()
		return m, textinput.Blink
	}
}

func (m *model) beginSettings() {
	m.screen = settingsScreen
	m.settingsCursor = 0
	m.outputPersist = false
	m.settingsPreset = m.preset
	m.outputInput.SetValue(m.outputRoot)
	m.outputInput.Focus()
	m.input.Blur()
	m.err, m.message = nil, ""
}

func (m *model) rescanPackList() tea.Cmd {
	m.loading = true
	m.loadingLabel = "Finding downloaded manga"
	m.err = nil
	root := m.outputRoot
	return func() tea.Msg {
		series, err := m.backend.DownloadedSeries(root)
		return downloadedSeriesMsg{series: series, err: err}
	}
}

func (m *model) rescanDownloads() tea.Cmd {
	m.loading = true
	m.loadingLabel = "Finding downloaded manga"
	m.err = nil
	root := m.outputRoot
	return func() tea.Msg {
		series, err := m.backend.DownloadedSeries(root)
		if err != nil {
			return downloadsStatusMsg{err: err}
		}
		statuses := make([]downloadStatus, 0, len(series))
		for _, dir := range series {
			recover, err := m.backend.PackNeedsRecovery(dir)
			if err != nil {
				return downloadsStatusMsg{err: err}
			}
			statuses = append(statuses, downloadStatus{dir: dir, recover: recover})
		}
		return downloadsStatusMsg{statuses: statuses}
	}
}

func (m model) anyInputFocused() bool {
	return m.input.Focused() || m.outputInput.Focused() || m.packPathInput.Focused() || m.filter.Focused() || m.rangeInput.Focused()
}

func (m model) Init() tea.Cmd {
	if m.plain {
		return textinput.Blink
	}
	return tea.Batch(textinput.Blink, m.spinner.Tick)
}
