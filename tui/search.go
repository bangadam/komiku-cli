// search.go handles series search and chapter discovery requests.
package tui

import (
	"fmt"
	"net/url"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

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

func isHTTPURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}
