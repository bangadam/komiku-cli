// chapters.go handles chapter selection, filtering, and range-based selection.
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bangadam/komiku-cli/cli"
	"github.com/bangadam/komiku-cli/komiku"
)

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
