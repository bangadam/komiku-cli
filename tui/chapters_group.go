// chapters_group.go groups chapters into Wikipedia volume rows and tracks grouped focus.
package tui

import (
	"fmt"
	"math"

	"github.com/bangadam/komiku-cli/komiku"
)

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
