// download.go tracks the download batch, its events, progress, and error tail.
package tui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bangadam/komiku-cli/cli"
	"github.com/bangadam/komiku-cli/kiryuu"
)

func (m model) startDownload() (tea.Model, tea.Cmd) {
	if m.flat {
		// Flat without a complete Wikipedia selection still packs as one
		// "Chapters X-Y" archive, so no gate is needed; only promote
		// when the selection does match complete Wikipedia volumes.
		if !m.explicitFlat && m.groupLoaded {
			resolved, err := cli.ResolveCompleteVolumeSelection(m.chapters, m.selected, m.groupMappings)
			if err == nil {
				m.applyVolumeJobs(resolved.Jobs, resolved.Mappings)
				m.mappingProvenance = "wikipedia-display"
				m.message = fmt.Sprintf("Promoted %d complete Wikipedia volume(s) to mapped download layout.", len(resolved.Mappings))
			}
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

func (m *model) addResultError(chapter kiryuu.Chapter, message string) {
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
