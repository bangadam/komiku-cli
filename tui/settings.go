// settings.go handles the settings screen and its focus management.
package tui

import (
	"errors"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

const settingsRows = 3

func (m model) updateSettings(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "down":
		m.settingsCursor = (m.settingsCursor + 1) % settingsRows
		m.syncSettingsFocus()
		m.err = nil
		return m, textinput.Blink
	case "up":
		m.settingsCursor = (m.settingsCursor + settingsRows - 1) % settingsRows
		m.syncSettingsFocus()
		m.err = nil
		return m, textinput.Blink
	case " ":
		if m.settingsCursor == 1 {
			m.outputPersist = !m.outputPersist
			m.err = nil
			return m, nil
		}
	case "left", "right":
		if m.settingsCursor == 2 {
			index := 0
			for i, preset := range presetCycle {
				if preset == m.settingsPreset {
					index = i
					break
				}
			}
			if key.String() == "right" {
				index = (index + 1) % len(presetCycle)
			} else {
				index = (index + len(presetCycle) - 1) % len(presetCycle)
			}
			m.settingsPreset = presetCycle[index]
			m.err = nil
		}
		return m, nil
	case "enter":
		path := strings.TrimSpace(m.outputInput.Value())
		if path == "" {
			m.err = errors.New("enter a download folder")
			m.settingsCursor = 0
			m.syncSettingsFocus()
			return m, textinput.Blink
		}
		resolved, err := m.backend.SetOutputRoot(path, m.outputPersist)
		if err != nil {
			m.err = err
			m.settingsCursor = 0
			m.syncSettingsFocus()
			return m, textinput.Blink
		}
		if err := m.backend.SetPreset(m.settingsPreset); err != nil {
			m.err = err
			return m, nil
		}
		m.outputRoot = resolved
		m.preset = m.settingsPreset
		m.err = nil
		m.message = "Settings saved."
		return m, nil
	case "esc":
		return m.switchNav(m.prevNav)
	case "q":
		if m.settingsCursor != 0 {
			return m, tea.Quit
		}
	}
	if input := m.settingsInputFor(m.settingsCursor); input != nil {
		input.Focus()
		var cmd tea.Cmd
		*input, cmd = input.Update(key)
		m.err = nil
		return m, cmd
	}
	m.outputInput.Blur()
	return m, nil
}

func (m *model) settingsInputFor(cursor int) *textinput.Model {
	switch cursor {
	case 0:
		return &m.outputInput
	}
	return nil
}

func (m *model) syncSettingsFocus() {
	inputs := []*textinput.Model{&m.outputInput}
	for _, input := range inputs {
		input.Blur()
	}
	if input := m.settingsInputFor(m.settingsCursor); input != nil {
		input.Focus()
	}
}
