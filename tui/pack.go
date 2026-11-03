// pack.go handles the pack and downloads screens, including standalone packing.
package tui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bangadam/komiku-cli/cli"
)

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
		m.packFromDownloads = false
		m.loading = true
		m.err = nil
		return m, func() tea.Msg {
			recover, err := m.backend.PackNeedsRecovery(dir)
			return packInspectMsg{seriesDir: dir, recover: recover, err: err}
		}
	case "esc":
		return m, m.rescanPackList()
	case "q":
		return m, tea.Quit
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
		if m.packFromDownloads {
			m.screen = downloadsScreen
		} else {
			m.screen = packSeriesScreen
		}
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
		target := 1
		if m.packFromDownloads {
			target = 2
		}
		return m.switchNav(target)
	case "q":
		return m, tea.Quit
	}
	return m, nil
}

func (m model) updateDownloads(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.loading {
		return m, nil
	}
	switch key.String() {
	case "up", "k":
		m.downloadsCursor = max(m.downloadsCursor-1, 0)
	case "down", "j":
		if len(m.downloads) > 0 {
			m.downloadsCursor = min(m.downloadsCursor+1, len(m.downloads)-1)
		}
	case "enter":
		if len(m.downloads) == 0 {
			m.err = errors.New("no downloaded manga found in this location")
			return m, nil
		}
		item := m.downloads[m.downloadsCursor]
		m.packFromDownloads = true
		m.loading = true
		m.err = nil
		return m, func() tea.Msg {
			recover, err := m.backend.PackNeedsRecovery(item.dir)
			return packInspectMsg{seriesDir: item.dir, recover: recover, err: err}
		}
	case "esc":
		return m, m.rescanDownloads()
	case "q":
		return m, tea.Quit
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
