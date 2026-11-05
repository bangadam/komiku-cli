// update.go routes Bubble Tea messages and owns operation lifecycle bookkeeping.
package tui

import (
	"context"
	"errors"
	"fmt"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bangadam/komiku-cli/cli"
	"github.com/bangadam/komiku-cli/kiryuu"
)

type searchMsg struct {
	id      uint64
	results []kiryuu.Series
	err     error
}

type discoverMsg struct {
	id        uint64
	seriesURL string
	chapters  []kiryuu.Chapter
	err       error
}

type volumeSelectionMsg struct {
	id         uint64
	volumes    []kiryuu.Volume
	jobs       []cli.Job
	provenance string
	err        error
}
type wikipediaVolumesMsg struct {
	id      uint64
	volumes []kiryuu.Volume
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

type downloadStatus struct {
	dir     string
	recover bool
}

type downloadsStatusMsg struct {
	statuses []downloadStatus
	err      error
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
		m.nav = 0
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
		m.groupMappings = append([]kiryuu.Volume(nil), msg.volumes...)
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
		m.nav = 0
		m.screen = chaptersScreen
		m.message = fmt.Sprintf("Selected %d chapters from %d volume(s).", len(msg.jobs), len(msg.volumes))
		return m, nil
	case downloadedSeriesMsg:
		m.loading = false
		m.err = msg.err
		if msg.err == nil {
			m.packSeries = msg.series
			m.packSeriesCursor = 0
			m.nav = 1
			m.screen = packSeriesScreen
		}
		return m, nil
	case downloadsStatusMsg:
		m.loading = false
		m.err = msg.err
		if msg.err == nil {
			m.downloads = msg.statuses
			m.downloadsCursor = 0
			m.nav = 2
			m.screen = downloadsScreen
		}
		return m, nil
	case packInspectMsg:
		m.loading = false
		m.err = msg.err
		if msg.err != nil {
			return m, nil
		}
		m.nav = 1
		if m.packFromDownloads {
			m.nav = 2
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
		m.nav = 1
		if m.packFromDownloads {
			m.nav = 2
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
		m.nav = 0
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
		m.nav = 0
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
		if m.screen != rangeScreen {
			switch msg.String() {
			case "tab":
				return m.switchNav((m.nav + 1) % len(navLabels))
			case "shift+tab":
				return m.switchNav((m.nav + len(navLabels) - 1) % len(navLabels))
			}
			if !m.anyInputFocused() {
				switch msg.String() {
				case "1":
					return m.switchNav(0)
				case "2":
					return m.switchNav(1)
				case "3":
					return m.switchNav(2)
				case "4":
					return m.switchNav(3)
				case "5":
					return m.switchNav(4)
				}
			}
		}
		if m.screen == settingsScreen {
			return m.updateSettings(msg)
		}
		switch m.screen {
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
		case downloadsScreen:
			return m.updateDownloads(msg)
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
