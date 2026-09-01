package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/bangadam/komiku-cli/cli"
)

func (m model) View() string {
	var lines []string
	switch m.screen {
	case settingsScreen:
		lines = m.settingsView()
	case searchScreen:
		lines = m.searchView()
	case chaptersScreen:
		lines = m.chaptersView()
	case rangeScreen:
		lines = m.rangeView()
	case downloadingScreen:
		lines = m.downloadingView()
	case doneScreen:
		lines = m.doneView()
	case packingScreen:
		lines = m.packingView()
	case packSeriesScreen:
		lines = m.packSeriesView()
	case packPathScreen:
		lines = m.packPathView()
	case packRecoveryScreen:
		lines = m.packRecoveryView()
	case standalonePackingScreen:
		lines = m.standalonePackingView()
	case standalonePackDoneScreen:
		lines = m.standalonePackDoneView()
	case downloadsScreen:
		lines = m.downloadsView()
	}
	return m.renderLayout(lines)
}

// asciiBox keeps bordered frames readable when the terminal strips styling.
var asciiBox = lipgloss.Border{
	Top:         "-",
	Bottom:      "-",
	Left:        "|",
	Right:       "|",
	TopLeft:     "+",
	TopRight:    "+",
	BottomLeft:  "+",
	BottomRight: "+",
}

func newViewStyles(plain bool, renderer *lipgloss.Renderer) viewStyles {
	styles := viewStyles{frame: lipgloss.NewStyle().Border(asciiBox)}
	if plain || renderer == nil {
		return styles
	}
	styles.frame = renderer.NewStyle().Border(lipgloss.RoundedBorder())
	styles.heading = renderer.NewStyle().Bold(true)
	styles.focus = renderer.NewStyle().Bold(true).Reverse(true)
	return styles
}

func (m model) renderLayout(lines []string) string {
	sidebarWidth, contentWidth := m.layoutWidths()
	compact := m.width < 48
	if compact {
		contentWidth = max(m.width-4, 1)
	}
	for index, line := range lines {
		if m.plain {
			line = asciiOnly(ansi.Strip(line))
		}
		lines[index] = ansi.Truncate(line, contentWidth, "...")
	}
	if compact {
		return m.styles.frame.Width(contentWidth+2).Padding(0, 1).Render(strings.Join(lines, "\n"))
	}
	sidebar := m.sidebarLines()
	for index, line := range sidebar {
		if m.plain {
			line = asciiOnly(ansi.Strip(line))
		}
		sidebar[index] = ansi.Truncate(line, sidebarWidth, "...")
	}
	height := max(len(lines), len(sidebar))
	sidebarBlock := m.styles.frame.Width(sidebarWidth+2).Height(height).Padding(0, 1).Render(strings.Join(sidebar, "\n"))
	contentBlock := m.styles.frame.Width(contentWidth+2).Height(height).Padding(0, 1).Render(strings.Join(lines, "\n"))
	return lipgloss.JoinHorizontal(lipgloss.Top, sidebarBlock, " ", contentBlock)
}

func (m model) layoutWidths() (int, int) {
	sidebar := min(16, max(m.width-12, 6))
	return sidebar, max(m.width-sidebar-9, 1)
}

func (m model) sidebarLines() []string {
	lines := []string{m.heading("komiku-cli"), ""}
	for index, label := range navLabels {
		if index == m.nav {
			lines = append(lines, m.accent("> "+label))
		} else {
			lines = append(lines, "  "+label)
		}
	}
	lines = append(lines, "", "Tab/ShiftTab nav")
	if m.outputRoot != "" {
		lines = append(lines, "", "Save to:", m.outputRoot)
	}
	return lines
}

func (m model) settingsView() []string {
	lines := []string{m.heading("Settings"), ""}
	folder := "  Download folder"
	if m.settingsCursor == 0 {
		folder = m.accent("> Download folder")
	}
	lines = append(lines, folder, "    "+m.outputInput.View())
	remember := "[ ] Remember this location"
	if m.outputPersist {
		remember = "[x] Remember this location"
	}
	if m.settingsCursor == 1 {
		remember = m.accent("> " + remember)
	} else {
		remember = "  " + remember
	}
	lines = append(lines, remember, "    Use this folder next time too.")
	preset := "  CBZ preset: " + string(m.settingsPreset)
	if m.settingsCursor == 2 {
		preset = m.accent("> CBZ preset: " + string(m.settingsPreset))
	}
	lines = append(lines, preset, "    Left/Right changes quality.")
	if m.err != nil {
		lines = append(lines, "", "[ERROR] "+m.err.Error())
	}
	if m.message != "" {
		lines = append(lines, m.message)
	}
	return append(lines, "", "Up/Down option  Space toggle  Enter save", "Tab next menu  Shift+Tab previous  Esc back")
}

func (m model) downloadsView() []string {
	lines := []string{m.heading("Downloads"), "From: " + m.outputRoot, ""}
	if m.loading {
		lines = append(lines, m.loadingLine(m.loadingLabel))
	}
	if len(m.downloads) == 0 && !m.loading {
		lines = append(lines, "No downloaded manga found here.", "Use Search to download chapters first.")
	}
	for index, item := range m.downloads {
		status := "ready to pack"
		if item.recover {
			status = "needs one-time recovery"
		}
		line := fmt.Sprintf("  %s  %s", filepath.Base(item.dir), status)
		if index == m.downloadsCursor {
			lines = append(lines, m.accent("> "+filepath.Base(item.dir)+"  "+status))
		} else {
			lines = append(lines, line)
		}
	}
	if m.err != nil {
		lines = append(lines, "", "[ERROR] "+m.err.Error())
	}
	return append(lines, "", "Up/Down move  Enter pack  Esc rescan  q quit")
}

func (m model) packSeriesView() []string {
	lines := []string{m.heading("Choose downloaded manga"), "From: " + m.outputRoot, ""}
	if len(m.packSeries) == 0 {
		lines = append(lines, "No downloaded manga found here.", "")
	} else {
		for index, dir := range m.packSeries {
			title := filepath.Base(dir)
			prefix := "  "
			if index == m.packSeriesCursor {
				lines = append(lines, m.accent("> "+title))
			} else {
				lines = append(lines, prefix+title)
			}
		}
		lines = append(lines, "")
	}
	other := "  Other folder..."
	if m.packSeriesCursor == len(m.packSeries) {
		other = m.accent("> Other folder...")
	}
	lines = append(lines, other, "    Enter a folder outside the active storage location.")
	if m.loading {
		lines = append(lines, "", m.loadingLine(m.loadingLabel))
	}
	if m.err != nil {
		lines = append(lines, "", "[ERROR] "+m.err.Error())
	}
	return append(lines, "", "Up/Down move  Enter select  Esc rescan")
}

func (m model) packPathView() []string {
	lines := []string{
		m.heading("Where is the downloaded manga?"),
		"Enter the series folder containing downloaded chapters.",
		"",
		"Series folder",
		m.packPathInput.View(),
	}
	if m.loading {
		lines = append(lines, "", m.loadingLine("Checking download"))
	}
	if m.err != nil {
		lines = append(lines, "", "[ERROR] "+m.err.Error())
	}
	return append(lines, "", "Enter continue  Esc back")
}

func (m model) packRecoveryView() []string {
	return []string{
		m.heading("Prepare old download for packing?"),
		"",
		filepath.Base(m.packSeriesDir),
		"This download was made before packing metadata existed.",
		"komiku-cli will use one Wikipedia lookup to find volume boundaries.",
		"Downloaded images stay local and will not be downloaded again.",
		"",
		m.accent("> Enter  Continue and pack"),
		"  Esc    Go back",
		"",
		"Tab nav  q quit",
	}
}

func (m model) standalonePackingView() []string {
	label := "Packing downloaded manga"
	if m.standalonePackRecover {
		label = "Preparing old download and packing"
	}
	return []string{m.heading("komiku-cli / Pack"), "", m.loadingLine(label), filepath.Base(m.packSeriesDir), "", "q stop safely after the current page"}
}

func (m model) standalonePackDoneView() []string {
	lines := []string{m.heading("Packing finished"), "", filepath.Base(m.packSeriesDir)}
	if m.err != nil {
		lines[0] = m.heading("Packing needs attention")
		lines = append(lines, "[ERROR] "+m.err.Error())
	} else {
		lines = append(lines, strings.Split(m.standalonePackOutput, "\n")...)
	}
	return append(lines, "", "Enter back to list  q quit")
}

func (m model) searchView() []string {
	lines := []string{m.heading("komiku-cli / Search")}
	if m.outputRoot != "" {
		lines = append(lines, "Save to: "+m.outputRoot)
	}
	lines = append(lines, "", m.input.View())
	switch {
	case m.loading:
		lines = append(lines, "", m.loadingLine(m.loadingLabel))
	case m.err != nil:
		lines = append(lines, "", "[ERROR] "+m.err.Error(), m.message)
	case m.emptyLabel != "":
		lines = append(lines, "", "[EMPTY] "+m.emptyLabel)
	case len(m.searchResults) > 0:
		lines = append(lines, "", fmt.Sprintf("Results (%d)", len(m.searchResults)))
		limit := max(m.height-11, 2)
		start, end := visibleWindow(m.searchCursor, len(m.searchResults), limit)
		for index := start; index < end; index++ {
			prefix := "  "
			if index == m.searchCursor {
				prefix = "> "
			}
			line := prefix + m.searchResults[index].Title + "  " + m.searchResults[index].Slug
			if index == m.searchCursor {
				line = m.accent(line)
			}
			lines = append(lines, line)
		}
		lines = append(lines, "", "Up/Down move  Enter open  Esc edit  Tab nav  q quit")
	default:
		lines = append(lines, "", "Enter searches titles. An HTTP(S) URL opens chapters directly.", "Tab switches the side menu. Ctrl+C quits.")
	}
	return lines
}

func (m model) chaptersView() []string {
	lines := []string{m.heading("komiku-cli / Chapters"), m.seriesURL, ""}
	if m.filtering || m.filter.Value() != "" {
		lines = append(lines, m.filter.View(), "")
	}
	layout := "flat"
	if !m.flat {
		layout = fmt.Sprintf("mapped (%d volume(s))", len(m.mappings))
	}
	if m.groupLoaded && m.flat {
		layout += "; complete Wikipedia volumes auto-map on Enter (r flat stays flat)"
	}
	tail := []string{"", fmt.Sprintf("Selected %d/%d  Layout %s", m.selectedCount(), len(m.chapters), layout)}
	if !m.flat {
		for _, mapping := range m.mappings {
			coverage := fmt.Sprintf("ch %d-%d", mapping.Start, mapping.End)
			if mapping.Start == mapping.End {
				coverage = fmt.Sprintf("ch %d", mapping.Start)
			}
			tail = append(tail, fmt.Sprintf("Volume %02d: %s", mapping.Volume, coverage))
		}
	}
	switch {
	case m.groupLoading:
		tail = append(tail, m.loadingLine("Loading volume groups from Wikipedia"))
	case m.groupErr != nil:
		tail = append(tail, "[ERROR] Wikipedia volume grouping: "+m.groupErr.Error(), "Press v to retry. No other source is used.")
	case m.groupEmpty:
		tail = append(tail, "[NO MAP] Wikipedia has no usable volume grouping.", "Press v to retry. No other source is used.")
	case m.groupView && m.groupLoaded:
		tail = append(tail, "Volume view source: Wikipedia")
	}
	if m.err != nil {
		tail = append(tail, "[ERROR] "+m.err.Error())
	}
	if m.message != "" {
		tail = append(tail, m.message)
	}
	volumeControl := "v volumes (Wikipedia)"
	toggleControl := "Space toggle"
	if m.groupView {
		volumeControl = "v flat (source Wikipedia)"
		if m.groupLoaded {
			toggleControl = "Space toggle chapter/volume"
		}
	}
	tail = append(tail, toggleControl+"  a  / filter  "+volumeControl+"  r range  Enter download  q quit")

	visible := m.visibleChapterIndices()
	if len(visible) == 0 {
		lines = append(lines, "[EMPTY] No chapters match the filter. Press / to edit or Esc while filtering to clear.")
	} else {
		limit := max(m.height-len(lines)-len(tail)-2, 1)
		if m.groupView && m.groupLoaded {
			rows := m.groupedRows(visible)
			focus := m.groupFocus
			focusRow := groupedFocusIndex(rows, focus)
			if focusRow < 0 {
				chapterRow := min(max(m.chapterCursor, 0), len(visible)-1)
				focus = groupFocus{kind: groupFocusChapter, chapterURL: m.chapters[visible[chapterRow]].URL}
				focusRow = groupedFocusIndex(rows, focus)
			}
			start, end := visibleWindow(max(focusRow, 0), len(rows), limit)
			for _, row := range rows[start:end] {
				focused := !m.filtering && sameGroupFocus(row.focus, focus)
				switch row.kind {
				case groupedVolumeRow:
					prefix := "  "
					if focused {
						prefix = "> "
					}
					line := prefix + m.wikipediaVolumeSelectionMark(row.volume) + " " + m.wikipediaVolumeLabel(row.volume)
					if focused {
						line = m.accent(line)
					}
					lines = append(lines, line)
				case groupedUnmappedRow:
					lines = append(lines, "Unmapped / extras")
				case groupedChapterRow:
					chapter := m.chapters[row.chapterIndex]
					mark := "[ ]"
					if m.selected[chapter.URL] {
						mark = "[x]"
					}
					prefix := "    "
					if focused {
						prefix = ">   "
					}
					line := fmt.Sprintf("%s%s ch %s  raw:%s", prefix, mark, chapter.Display, chapter.RawID)
					if focused {
						line = m.accent(line)
					}
					lines = append(lines, line)
				}
			}
		} else {
			start, end := visibleWindow(m.chapterCursor, len(visible), limit)
			for row := start; row < end; row++ {
				chapter := m.chapters[visible[row]]
				mark := "[ ]"
				if m.selected[chapter.URL] {
					mark = "[x]"
				}
				prefix := "  "
				if row == m.chapterCursor && !m.filtering {
					prefix = "> "
				}
				line := fmt.Sprintf("%s%s ch %s  raw:%s", prefix, mark, chapter.Display, chapter.RawID)
				if row == m.chapterCursor && !m.filtering {
					line = m.accent(line)
				}
				lines = append(lines, line)
			}
		}
	}
	return append(lines, tail...)
}

func (m model) wikipediaVolumeLabel(volume int) string {
	for _, mapping := range m.groupMappings {
		if mapping.Volume == volume {
			return fmt.Sprintf("Volume %02d  ch %d-%d", volume, mapping.Start, mapping.End)
		}
	}
	return fmt.Sprintf("Volume %02d", volume)
}

func (m model) rangeView() []string {
	modes := []string{"chapter", "volume", "manual", "flat"}
	modeLabels := make([]string, len(modes))
	for index, label := range modes {
		if rangeMode(index) == m.rangeMode {
			modeLabels[index] = "[" + label + "]"
		} else {
			modeLabels[index] = label
		}
	}
	lines := []string{
		m.heading("komiku-cli / Range"),
		"",
		"> Mode: " + strings.Join(modeLabels, "  "),
		"Tab/Left/Right changes mode. Escape cancels.",
		"",
	}
	switch m.rangeMode {
	case chapterRange:
		lines = append(lines, "Chapter: 1-50,271.5 or all. Only discovered chapters are selected.")
	case volumeRange:
		lines = append(lines, "Volume: 7-12. Uses cached or automatic mapping.")
	case manualRange:
		lines = append(lines, "Manual: 1:1-7,2:8-15 | 1-2. Left is mapping; right selects volumes.")
	case flatRange:
		lines = append(lines, "Flat: 1-50,271.5 or all. No volume folders are invented.")
	}
	lines = append(lines, m.rangeInput.View())
	if m.rangeLoading {
		lines = append(lines, "", m.loadingLine("Loading and validating mapping"))
	}
	if m.err != nil {
		lines = append(lines, "", "[ERROR] "+m.err.Error(), "Correct the value or press Escape.")
	}
	lines = append(lines, "", "Enter applies  Escape cancels")
	return lines
}

func (m model) downloadingView() []string {
	total := len(m.jobs)
	percent := 0.0
	if total > 0 {
		percent = float64(m.completed) / float64(total)
	}
	lines := []string{m.heading("komiku-cli / Download"), "", m.progress.ViewAs(percent)}
	lines = append(lines, fmt.Sprintf("Chapters %d/%d", m.completed, total))
	if m.latest.URL != "" {
		page := "page unknown"
		if m.latestPages > 0 {
			page = fmt.Sprintf("page %d/%d", m.latestPage, m.latestPages)
		}
		lines = append(lines, fmt.Sprintf("Current ch %s  %s", m.latest.Display, page))
	}
	lines = append(lines, fmt.Sprintf("Speed %s  ETA %s  Errors %d", m.speed(), m.eta(), m.errorCount))
	if m.message != "" {
		status := m.message
		if m.paused {
			status = "[PAUSED] " + status
		} else if m.stopping {
			status = "[STOPPING] " + status
		}
		lines = append(lines, status)
	}
	if len(m.tail) > 0 {
		lines = append(lines, "", "Recent")
		lines = append(lines, m.tail...)
	}
	lines = append(lines, "", "Space pause/resume  Tab nav  q stop safely")
	return lines
}

func (m model) doneView() []string {
	counts := m.summary.Counts
	lines := []string{
		m.heading("komiku-cli / Done"),
		"",
		fmt.Sprintf("[DONE] %d  [PART] %d  [FAIL] %d  [NOIMG] %d", counts[cli.Done], counts[cli.Part], counts[cli.Fail], counts[cli.NoImg]),
		fmt.Sprintf("Pages ok %d  failed %d  Errors %d", m.summary.PagesOK, m.summary.PagesFailed, m.errorCount),
		"Output: " + m.summary.OutputDir,
		"Audit:  " + m.summary.AuditPath,
	}
	if m.summary.Err != nil {
		lines = append(lines, "[ERROR] Finalize download: "+m.summary.Err.Error())
	}
	if m.summary.Cancelled {
		lines = append(lines, fmt.Sprintf("Stopped safely after starting %d/%d chapters.", m.summary.Started, m.summary.Requested))
	}
	for _, skipped := range m.packPlan.Skipped {
		lines = append(lines, fmt.Sprintf("[PACK DISABLED] Volume %02d: %s", skipped.Volume, skipped.Reason))
	}
	if m.packPlan.DisabledReason != "" {
		lines = append(lines, "[PACK DISABLED] "+m.packPlan.DisabledReason)
	} else {
		lines = append(lines, m.accent(fmt.Sprintf("> p Pack %d volume(s), preset %s", len(m.packPlan.Volumes), m.packPlan.Preset)))
	}
	for _, outcome := range sortedPackOutcomes(m.packOutcomes) {
		if outcome.Err != nil {
			lines = append(lines, fmt.Sprintf("[PACK FAILED] Volume %02d: %v", outcome.Volume, outcome.Err))
			continue
		}
		lines = append(lines, fmt.Sprintf("[PACKED] Volume %02d: %s preset=%s", outcome.Volume, outcome.Result.Path, outcome.Result.Preset))
		for _, warning := range outcome.Result.Warnings {
			lines = append(lines, fmt.Sprintf("[WARNING] Volume %02d: %s", outcome.Volume, warning.Error()))
		}
	}
	if m.packErr != nil {
		lines = append(lines, "[ERROR] Pack lifecycle: "+m.packErr.Error())
	}
	if m.message != "" {
		lines = append(lines, m.message)
	}
	lines = append(lines, "", "p pack  Tab nav  q quit")
	return lines
}

func (m model) packingView() []string {
	lines := []string{
		m.heading("komiku-cli / Pack"),
		"",
		m.loadingLine(fmt.Sprintf("Packing %d volume(s), preset %s", len(m.packPlan.Volumes), m.packPlan.Preset)),
		m.message,
		"",
		"q stop safely after the current page",
	}
	return lines
}

func (m model) speed() string {
	elapsed := m.activeElapsed()
	if elapsed <= 0 || m.bytes == 0 {
		return "--"
	}
	return fmt.Sprintf("%.1f KiB/s", float64(m.bytes)/1024/elapsed.Seconds())
}

func (m model) eta() string {
	if m.completed == 0 || m.startedAt.IsZero() || len(m.jobs) <= m.completed {
		if len(m.jobs) == m.completed && m.completed > 0 {
			return "0s"
		}
		return "--"
	}
	elapsed := m.activeElapsed()
	if elapsed <= 0 {
		return "--"
	}
	remaining := len(m.jobs) - m.completed
	value := time.Duration(float64(elapsed) / float64(m.completed) * float64(remaining)).Round(time.Second)
	return value.String()
}

func (m model) activeElapsed() time.Duration {
	if m.startedAt.IsZero() {
		return 0
	}
	end := m.now()
	paused := m.pausedFor
	if m.paused && !m.pausedAt.IsZero() {
		paused += end.Sub(m.pausedAt)
	}
	elapsed := end.Sub(m.startedAt) - paused
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

func (m model) loadingLine(label string) string {
	if m.plain {
		return "[LOADING] " + label + "..."
	}
	return m.spinner.View() + " " + label + "..."
}

func (m model) heading(value string) string {
	return m.styles.heading.Render(value)
}

func (m model) accent(value string) string {
	return m.styles.focus.Render(value)
}

func visibleWindow(cursor, total, limit int) (int, int) {
	if total <= limit {
		return 0, total
	}
	start := cursor - limit/2
	if start < 0 {
		start = 0
	}
	if start+limit > total {
		start = total - limit
	}
	return start, start + limit
}

func asciiOnly(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for _, r := range value {
		switch {
		case r == '\n' || r == '\t' || (r >= 32 && r <= 126):
			builder.WriteRune(r)
		case unicode.IsSpace(r):
			builder.WriteByte(' ')
		default:
			builder.WriteByte('?')
		}
	}
	return builder.String()
}
