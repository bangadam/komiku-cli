package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bangadam/komiku-cli/cli"
	"github.com/bangadam/komiku-cli/komiku"
	packer "github.com/bangadam/komiku-cli/pack"
	"github.com/bangadam/komiku-cli/store"
)

type fakeBackend struct {
	searchResults         []komiku.Series
	chapters              []komiku.Chapter
	searchErr             error
	discoverErr           error
	volumes               []komiku.Volume
	wikipediaVolumes      []komiku.Volume
	wikipediaVolumesErr   error
	wikipediaVolumeCalls  int
	wikipediaVolumeSeries string
	batch                 *fakeBatch
	packPlan              cli.PackPlan
	packStore             *store.SeriesStore
	preparedMappings      []komiku.Volume
	preparedJobs          []cli.Job
	recordedProvenance    string
	recordErr             error
	packOutcomes          []cli.PackOutcome
	packErr               error
	searchQuery           string
	discoverURL           string
	savedVolumes          []komiku.Volume
	saveCalls             int
	startedJobs           []cli.Job
	startCalls            int
	outputRoot            string
	outputPersist         bool
	outputSetCalls        int
	outputSetErr          error
	downloadedSeries      []string
	downloadedSeriesErr   error
	packNeedsRecovery     bool
	packInspectErr        error
	standalonePackOutput  string
	standalonePackErr     error
	standalonePackCalls   int
	standalonePackDir     string
	standalonePackRecover bool
	presetSaved           packer.Preset
	presetCalls           int
	presetErr             error
}

func (f *fakeBackend) Search(_ context.Context, query string) ([]komiku.Series, error) {
	f.searchQuery = query
	return f.searchResults, f.searchErr
}
func (f *fakeBackend) SetOutputRoot(path string, persist bool) (string, error) {
	f.outputSetCalls++
	f.outputRoot = path
	f.outputPersist = persist
	return path, f.outputSetErr
}
func (f *fakeBackend) SetPreset(preset packer.Preset) error {
	f.presetCalls++
	f.presetSaved = preset
	return f.presetErr
}
func (f *fakeBackend) DownloadedSeries(string) ([]string, error) {
	return append([]string(nil), f.downloadedSeries...), f.downloadedSeriesErr
}
func (f *fakeBackend) PackNeedsRecovery(string) (bool, error) {
	return f.packNeedsRecovery, f.packInspectErr
}
func (f *fakeBackend) PackDownloaded(_ context.Context, seriesDir string, recover bool, _ packer.Preset) (string, error) {
	f.standalonePackCalls++
	f.standalonePackDir = seriesDir
	f.standalonePackRecover = recover
	return f.standalonePackOutput, f.standalonePackErr
}
func (f *fakeBackend) Discover(_ context.Context, seriesURL string) ([]komiku.Chapter, error) {
	f.discoverURL = seriesURL
	return f.chapters, f.discoverErr
}
func (f *fakeBackend) LoadVolumes(context.Context, string, []komiku.Chapter) ([]komiku.Volume, error) {
	return f.volumes, nil
}
func (f *fakeBackend) LoadWikipediaVolumes(_ context.Context, seriesURL string) ([]komiku.Volume, error) {
	f.wikipediaVolumeCalls++
	f.wikipediaVolumeSeries = seriesURL
	return f.wikipediaVolumes, f.wikipediaVolumesErr
}
func (f *fakeBackend) SaveManualVolumes(_ string, volumes []komiku.Volume, _ int) error {
	f.saveCalls++
	f.savedVolumes = append([]komiku.Volume(nil), volumes...)
	return nil
}
func (f *fakeBackend) Start(_ context.Context, _ string, jobs []cli.Job) (batchRun, error) {
	f.startCalls++
	f.startedJobs = append([]cli.Job(nil), jobs...)
	if f.batch == nil {
		return nil, errors.New("missing fake batch")
	}
	return f.batch, nil
}
func (f *fakeBackend) RecordPackManifest(seriesURL, provenance string, mappings []komiku.Volume, jobs []cli.Job, results []cli.Result) error {
	f.recordedProvenance = provenance
	if f.recordErr != nil {
		return f.recordErr
	}
	if f.packStore != nil {
		return cli.RecordPackManifest(f.packStore.Dir(), f.packStore.Series, seriesURL, provenance, mappings, jobs, results)
	}
	return nil
}
func (f *fakeBackend) PreparePack(_ string, preset packer.Preset, mappings []komiku.Volume, jobs []cli.Job, results []cli.Result) cli.PackPlan {
	f.preparedMappings = append([]komiku.Volume(nil), mappings...)
	f.preparedJobs = append([]cli.Job(nil), jobs...)
	if f.packStore != nil {
		return cli.PreparePack(f.packStore, f.packStore.Series, preset, mappings, jobs, results)
	}
	return f.packPlan
}
func (f *fakeBackend) Pack(context.Context, cli.PackPlan) ([]cli.PackOutcome, error) {
	return f.packOutcomes, f.packErr
}

type fakeBatch struct {
	events      chan cli.Event
	summary     cli.Summary
	pauseCalls  int
	resumeCalls int
	cancelCalls int
}

func (f *fakeBatch) Events() <-chan cli.Event { return f.events }
func (f *fakeBatch) Pause()                   { f.pauseCalls++ }
func (f *fakeBatch) Resume()                  { f.resumeCalls++ }
func (f *fakeBatch) Cancel()                  { f.cancelCalls++ }
func (f *fakeBatch) Wait() cli.Summary        { return f.summary }

func updateModel(t *testing.T, current model, message tea.Msg) (model, tea.Cmd) {
	t.Helper()
	updated, cmd := current.Update(message)
	return updated.(model), cmd
}

func TestSidebarDefaultsToSearch(t *testing.T) {
	current := newModel(&fakeBackend{}, packer.Raw, true, nil, time.Now)
	current.outputRoot = "/manga"
	if current.nav != 0 || current.screen != searchScreen || !current.input.Focused() {
		t.Fatalf("startup state nav=%d screen=%v focused=%v", current.nav, current.screen, current.input.Focused())
	}
	view := current.View()
	for _, want := range []string{"komiku-cli", "> Search", "  To CBZ", "  Downloads", "  Settings", "Save to:"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q in sidebar:\n%s", want, view)
		}
	}
}

func TestTabCyclesNavMenusAndLoadsLists(t *testing.T) {
	service := &fakeBackend{downloadedSeries: []string{"/manga/sakamoto-days"}, standalonePackOutput: "packed"}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.outputRoot = "/manga"
	current, cmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyTab})
	if current.nav != 1 || current.screen != packSeriesScreen || cmd == nil {
		t.Fatalf("tab did not open To CBZ: nav=%d screen=%v cmd=%v", current.nav, current.screen, cmd)
	}
	current, _ = updateModel(t, current, cmd())
	if !strings.Contains(current.View(), "sakamoto-days") {
		t.Fatalf("To CBZ list missing series:\n%s", current.View())
	}
	current, cmd = updateModel(t, current, tea.KeyMsg{Type: tea.KeyTab})
	if current.nav != 2 || current.screen != downloadsScreen || cmd == nil {
		t.Fatalf("tab did not open Downloads: nav=%d screen=%v cmd=%v", current.nav, current.screen, cmd)
	}
	current, _ = updateModel(t, current, cmd())
	if !strings.Contains(current.View(), "ready to pack") {
		t.Fatalf("Downloads list missing status:\n%s", current.View())
	}
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyTab})
	if current.nav != 3 || current.screen != settingsScreen || !current.outputInput.Focused() || current.outputInput.Value() != "/manga" {
		t.Fatalf("tab did not open Settings: nav=%d screen=%v value=%q", current.nav, current.screen, current.outputInput.Value())
	}
	if !strings.Contains(current.View(), "CBZ preset: raw") {
		t.Fatalf("settings missing preset:\n%s", current.View())
	}
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyTab})
	if current.nav != 0 || current.screen != searchScreen || !current.input.Focused() || current.outputInput.Focused() {
		t.Fatalf("tab from Settings did not return to focused Search: nav=%d screen=%v search-focused=%v settings-focused=%v", current.nav, current.screen, current.input.Focused(), current.outputInput.Focused())
	}
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyShiftTab})
	if current.nav != 3 || current.screen != settingsScreen || !current.outputInput.Focused() {
		t.Fatalf("shift tab from Search did not return to Settings: nav=%d screen=%v focused=%v", current.nav, current.screen, current.outputInput.Focused())
	}
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyEsc})
	if current.nav != 0 || current.screen != searchScreen {
		t.Fatalf("settings esc did not return to Search: nav=%d screen=%v", current.nav, current.screen)
	}
}

func TestDigitShortcutsNeedUnfocusedInput(t *testing.T) {
	service := &fakeBackend{downloadedSeries: []string{"/manga/series"}}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.outputRoot = "/manga"
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	if current.input.Value() != "2" || current.nav != 0 {
		t.Fatalf("focused input lost digit: value=%q nav=%d", current.input.Value(), current.nav)
	}
	current.input.Blur()
	current, cmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	if current.nav != 1 || cmd == nil {
		t.Fatalf("digit 2 did not open To CBZ: nav=%d cmd=%v", current.nav, cmd)
	}
	current, _ = updateModel(t, current, cmd())
	if current.screen != packSeriesScreen {
		t.Fatalf("digit nav list missing: screen=%v", current.screen)
	}
}

func TestNavToCBZLegacyConfirmation(t *testing.T) {
	service := &fakeBackend{downloadedSeries: []string{"/manga/sakamoto-days"}, packNeedsRecovery: true, standalonePackOutput: "packed"}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.outputRoot = "/manga"
	_, listCmd := current.switchNav(1)
	current, _ = updateModel(t, current, listCmd())
	current, inspectCmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyEnter})
	current, _ = updateModel(t, current, inspectCmd())
	if current.screen != packRecoveryScreen || !strings.Contains(current.View(), "one Wikipedia lookup") || service.standalonePackCalls != 0 {
		t.Fatalf("legacy confirmation missing: state=%+v calls=%d\n%s", current, service.standalonePackCalls, current.View())
	}
	current, packCmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyEnter})
	if packCmd == nil || current.screen != standalonePackingScreen {
		t.Fatalf("confirmation did not start pack: state=%+v", current)
	}
	current, _ = updateModel(t, current, packCmd())
	if current.screen != standalonePackDoneScreen || service.standalonePackCalls != 1 || !service.standalonePackRecover || service.standalonePackDir != "/manga/sakamoto-days" || current.nav != 1 {
		t.Fatalf("legacy pack result wrong: state=%+v service=%+v", current, service)
	}
	_, backCmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyEnter})
	if backCmd == nil {
		t.Fatal("done enter did not rescan To CBZ list")
	}
}

func TestNavToCBZManifestSeriesPacksOffline(t *testing.T) {
	service := &fakeBackend{downloadedSeries: []string{"/manga/manifest-series"}, standalonePackOutput: "packed offline"}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.outputRoot = "/manga"
	_, listCmd := current.switchNav(1)
	current, _ = updateModel(t, current, listCmd())
	current, inspectCmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyEnter})
	current, packCmd := updateModel(t, current, inspectCmd())
	if current.screen != standalonePackingScreen || packCmd == nil {
		t.Fatalf("manifest pack did not start offline: state=%+v", current)
	}
	current, _ = updateModel(t, current, packCmd())
	if current.screen != standalonePackDoneScreen || service.standalonePackRecover || !strings.Contains(current.View(), "packed offline") || current.nav != 1 {
		t.Fatalf("offline result wrong: state=%+v\n%s", current, current.View())
	}
}

func TestToCBZOtherFolderAcceptsTypedPath(t *testing.T) {
	service := &fakeBackend{standalonePackOutput: "packed elsewhere"}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.outputRoot = "/manga"
	_, listCmd := current.switchNav(1)
	current, _ = updateModel(t, current, listCmd())
	if !strings.Contains(current.View(), "Other folder...") {
		t.Fatalf("other folder choice missing:\n%s", current.View())
	}
	current.packSeriesCursor = len(current.packSeries)
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyEnter})
	if current.screen != packPathScreen || !current.packPathInput.Focused() {
		t.Fatalf("other folder did not open path input: state=%+v", current)
	}
	for _, character := range "/archive/manga with q" {
		current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{character}})
	}
	if current.packPathInput.Value() != "/archive/manga with q" {
		t.Fatalf("path input rejected ordinary characters: %q", current.packPathInput.Value())
	}
	current, inspectCmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyEnter})
	current, packCmd := updateModel(t, current, inspectCmd())
	current, _ = updateModel(t, current, packCmd())
	if service.standalonePackDir != "/archive/manga with q" || current.screen != standalonePackDoneScreen {
		t.Fatalf("typed path pack wrong: dir=%q screen=%v", service.standalonePackDir, current.screen)
	}
}

func TestDownloadsShowStatusesAndPackShortcuts(t *testing.T) {
	service := &fakeBackend{downloadedSeries: []string{"/manga/needs-recovery", "/manga/manifest-series"}, packNeedsRecovery: true, standalonePackOutput: "packed"}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.outputRoot = "/manga"
	_, statusCmd := current.switchNav(2)
	current, _ = updateModel(t, current, statusCmd())
	view := current.View()
	if !strings.Contains(view, "needs-recovery  needs one-time recovery") || !strings.Contains(view, "manifest-series  needs one-time recovery") {
		t.Fatalf("status rows missing:\n%s", view)
	}
	current, inspectCmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyEnter})
	current, _ = updateModel(t, current, inspectCmd())
	if current.screen != packRecoveryScreen || current.nav != 2 || !current.packFromDownloads {
		t.Fatalf("downloads pack did not open recovery: state=%+v", current)
	}
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyEsc})
	if current.screen != downloadsScreen {
		t.Fatalf("recovery esc did not return to downloads: screen=%v", current.screen)
	}
}

func TestDownloadsDirectPackReturnsToDownloads(t *testing.T) {
	service := &fakeBackend{downloadedSeries: []string{"/manga/manifest-series"}, standalonePackOutput: "packed offline"}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.outputRoot = "/manga"
	_, statusCmd := current.switchNav(2)
	current, _ = updateModel(t, current, statusCmd())
	current, inspectCmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyEnter})
	current, packCmd := updateModel(t, current, inspectCmd())
	if current.screen != standalonePackingScreen || packCmd == nil || service.standalonePackRecover {
		t.Fatalf("downloads direct pack wrong: state=%+v service=%+v", current, service)
	}
	current, _ = updateModel(t, current, packCmd())
	if current.screen != standalonePackDoneScreen || current.nav != 2 {
		t.Fatalf("downloads pack done wrong: state=%+v", current)
	}
	current, backCmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyEnter})
	if backCmd == nil || current.screen != downloadsScreen || current.packFromDownloads || current.nav != 2 {
		t.Fatalf("done enter did not return to downloads: state=%+v cmd=%v", current, backCmd)
	}
	current, _ = updateModel(t, current, backCmd())
	if current.screen != downloadsScreen || current.packFromDownloads {
		t.Fatalf("done return wrong: state=%+v", current)
	}
}

func TestSettingsPrefillFocusAndSave(t *testing.T) {
	service := &fakeBackend{}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.outputRoot = "/configured/manga"
	for i := 0; i < 3; i++ {
		current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyTab})
	}
	view := current.View()
	for _, want := range []string{"Settings", "/configured/manga", "[ ] Remember this location", "CBZ preset: raw", "Up/Down option", "Tab next menu", "Enter save"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q in settings:\n%s", want, view)
		}
	}
	if !current.outputInput.Focused() {
		t.Fatal("settings path input is not focused")
	}
	current.outputInput.SetValue("")
	for _, character := range "/new/manga" {
		current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{character}})
	}
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyDown})
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeySpace})
	if !strings.Contains(current.View(), "[x] Remember this location") {
		t.Fatalf("remember toggle missing:\n%s", current.View())
	}
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyDown})
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyRight})
	if !strings.Contains(current.View(), "CBZ preset: medium") {
		t.Fatalf("preset cycling missing:\n%s", current.View())
	}
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyLeft})
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyLeft})
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyEnter})
	if service.outputRoot != "/new/manga" || !service.outputPersist || service.outputSetCalls != 1 || service.presetSaved != packer.Tiny || service.presetCalls != 1 {
		t.Fatalf("settings save wrong: service=%+v", service)
	}
	if current.outputRoot != "/new/manga" || current.preset != packer.Tiny || current.screen != settingsScreen || !strings.Contains(current.View(), "Settings saved.") {
		t.Fatalf("settings state after save: state=%+v\n%s", current, current.View())
	}
}

func TestSettingsKeepsEditingErrorsInline(t *testing.T) {
	service := &fakeBackend{outputSetErr: errors.New("location is not writable")}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.outputRoot = "/invalid"
	for i := 0; i < 3; i++ {
		current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyTab})
	}
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyEnter})
	if current.screen != settingsScreen || current.settingsCursor != 0 || !current.outputInput.Focused() || service.presetCalls != 0 || !strings.Contains(current.View(), "location is not writable") {
		t.Fatalf("invalid location escaped settings: state=%+v\n%s", current, current.View())
	}
	current.outputInput.SetValue("")
	for _, character := range "Manga queue" {
		current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{character}})
	}
	if current.outputInput.Value() != "Manga queue" {
		t.Fatalf("folder input rejected ordinary characters: %q", current.outputInput.Value())
	}
	current.settingsCursor = 1
	_, cmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("q did not quit settings while not editing")
	}
}

func TestSettingsEscReturnsToPreviousView(t *testing.T) {
	service := &fakeBackend{downloadedSeries: []string{"/manga/series"}}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.outputRoot = "/manga"
	_, listCmd := current.switchNav(1)
	current, _ = updateModel(t, current, listCmd())
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyTab})
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyTab})
	if current.nav != 3 || current.screen != settingsScreen {
		t.Fatalf("settings not opened: nav=%d screen=%v", current.nav, current.screen)
	}
	current, backCmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyEsc})
	if backCmd == nil || current.nav != 2 {
		t.Fatalf("settings esc did not target Downloads: nav=%d cmd=%v", current.nav, backCmd)
	}
	current, _ = updateModel(t, current, backCmd())
	if current.screen != downloadsScreen {
		t.Fatalf("return list missing: screen=%v", current.screen)
	}
}

func TestToCBZEscRescansList(t *testing.T) {
	service := &fakeBackend{downloadedSeries: []string{"/manga/series"}}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.outputRoot = "/manga"
	_, listCmd := current.switchNav(1)
	current, _ = updateModel(t, current, listCmd())
	current, rescanCmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyEsc})
	if rescanCmd == nil {
		t.Fatal("esc did not rescan To CBZ list")
	}
	current, _ = updateModel(t, current, rescanCmd())
	if current.screen != packSeriesScreen || !strings.Contains(current.View(), "series") {
		t.Fatalf("rescan lost list: screen=%v\n%s", current.screen, current.View())
	}
}

func TestModelKeywordSearchUsesSelectedResultURL(t *testing.T) {
	resultURL := "https://fixture.invalid/manga/actual-series/"
	service := &fakeBackend{
		searchResults: []komiku.Series{{Title: "Actual Series", Slug: "actual-series", URL: resultURL, Href: "/manga/actual-series/"}},
		chapters:      []komiku.Chapter{{RawID: "271.5", Display: "271.5", Number: 271.5, URL: resultURL + "chapter-271-5/"}},
	}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.input.SetValue("actual")
	var cmd tea.Cmd
	current, cmd = updateModel(t, current, tea.KeyMsg{Type: tea.KeyEnter})
	current, _ = updateModel(t, current, cmd())
	if service.searchQuery != "actual" || len(current.searchResults) != 1 {
		t.Fatalf("query=%q results=%+v", service.searchQuery, current.searchResults)
	}
	current, cmd = updateModel(t, current, tea.KeyMsg{Type: tea.KeyEnter})
	current, _ = updateModel(t, current, cmd())
	if service.discoverURL != resultURL || current.screen != chaptersScreen || current.chapters[0].RawID != "271.5" {
		t.Fatalf("discover=%q screen=%v chapters=%+v", service.discoverURL, current.screen, current.chapters)
	}
}

func TestModelFilterAndManualRangePreserveDiscoveredIdentity(t *testing.T) {
	chapters := []komiku.Chapter{
		{RawID: "1", Display: "1", Number: 1, URL: "https://fixture.invalid/chapter/custom-a/"},
		{RawID: "2", Display: "2", Number: 2, URL: "https://fixture.invalid/chapter/custom-b/"},
		{RawID: "271.5", Display: "271.5", Number: 271.5, URL: "https://fixture.invalid/chapter/extra/"},
	}
	service := &fakeBackend{}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.screen, current.seriesURL, current.chapters = chaptersScreen, "https://fixture.invalid/manga/series/", chapters
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("271.5")})
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyEnter})
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeySpace})
	if !current.selected[chapters[2].URL] {
		t.Fatalf("filtered selection lost actual URL: %+v", current.selected)
	}

	current.filter.SetValue("")
	current.chapters = chapters[:2]
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	current.rangeMode = manualRange
	current.rangeInput.SetValue("1:1-2 | 1")
	ranged, cmd := current.applyRange()
	current = ranged.(model)
	current, _ = updateModel(t, current, cmd())
	if current.screen != chaptersScreen || current.flat || len(service.savedVolumes) != 1 {
		t.Fatalf("manual range state=%+v saved=%+v", current, service.savedVolumes)
	}
	jobs := current.selectedJobs()
	if len(jobs) != 2 || jobs[0].Chapter.URL != chapters[0].URL || jobs[1].Chapter.URL != chapters[1].URL || jobs[0].Volume != 1 || jobs[1].Volume != 1 {
		t.Fatalf("jobs=%+v", jobs)
	}
}
func TestVolumeViewToggleLoadsWikipediaWithoutChangingDownloadLayout(t *testing.T) {
	chapters := []komiku.Chapter{
		{RawID: "1", Display: "1", Number: 1, URL: "one"},
		{RawID: "2", Display: "2", Number: 2, URL: "two"},
		{RawID: "3", Display: "3", Number: 3, URL: "three"},
	}
	service := &fakeBackend{wikipediaVolumes: []komiku.Volume{{Volume: 1, Start: 1, End: 2}}}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.screen, current.seriesURL, current.chapters = chaptersScreen, "https://fixture.invalid/manga/series/", chapters
	current.flat = false
	current.selected[chapters[1].URL] = true
	current.assignments[chapters[1].URL] = 9
	current.mappings = []komiku.Volume{{Volume: 9, Start: 2, End: 2}}
	beforeJobs := current.selectedJobs()

	current, cmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	if cmd == nil {
		t.Fatal("v did not start the Wikipedia volume lookup")
	}
	current, _ = updateModel(t, current, cmd())

	if service.wikipediaVolumeCalls != 1 {
		t.Fatalf("Wikipedia lookup calls = %d, want 1", service.wikipediaVolumeCalls)
	}
	if !strings.Contains(current.View(), "Volume 01  ch 1-2") || !strings.Contains(current.View(), "Unmapped / extras") {
		t.Fatalf("grouped view missing headings:\n%s", current.View())
	}
	afterJobs := current.selectedJobs()
	if current.flat || !current.selected[chapters[1].URL] || current.assignments[chapters[1].URL] != 9 || len(current.mappings) != 1 || current.mappings[0].Volume != 9 || service.saveCalls != 0 {
		t.Fatalf("display toggle changed download state: %+v", current)
	}
	if len(beforeJobs) != 1 || len(afterJobs) != 1 || beforeJobs[0].Chapter.URL != afterJobs[0].Chapter.URL || beforeJobs[0].Volume != afterJobs[0].Volume || beforeJobs[0].Flat != afterJobs[0].Flat {
		t.Fatalf("display toggle changed selected jobs: before=%+v after=%+v", beforeJobs, afterJobs)
	}

	current, cmd = updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	if cmd != nil || strings.Contains(current.View(), "Volume 01  ch 1-2") || !current.selected[chapters[1].URL] {
		t.Fatalf("second v did not restore the flat display without changing selection: cmd=%v\n%s", cmd != nil, current.View())
	}
}
func TestWikipediaVolumeViewStatesRetryCacheAndIgnoreStaleResults(t *testing.T) {
	chapter := komiku.Chapter{RawID: "1", Display: "1", Number: 1, URL: "one"}
	service := &fakeBackend{wikipediaVolumesErr: errors.New("Wikipedia unavailable")}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.screen, current.seriesURL, current.chapters = chaptersScreen, "https://fixture.invalid/manga/series/", []komiku.Chapter{chapter}

	current, cmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	if !current.groupView || !current.groupLoading || !strings.Contains(current.View(), "Loading volume groups from Wikipedia") {
		t.Fatalf("loading state missing: %+v\n%s", current, current.View())
	}
	current, _ = updateModel(t, current, cmd())
	if current.groupView || current.groupErr == nil || !strings.Contains(current.View(), "Press v to retry. No other source is used.") {
		t.Fatalf("recoverable Wikipedia error missing: %+v\n%s", current, current.View())
	}

	service.wikipediaVolumesErr = nil
	service.wikipediaVolumes = []komiku.Volume{{Volume: 1, Start: 1, End: 1}}
	current, cmd = updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	current, _ = updateModel(t, current, cmd())
	if service.wikipediaVolumeCalls != 2 || !current.groupLoaded || !current.groupView {
		t.Fatalf("retry did not load once: calls=%d state=%+v", service.wikipediaVolumeCalls, current)
	}
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	current, cmd = updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	if cmd != nil || service.wikipediaVolumeCalls != 2 || !current.groupView {
		t.Fatalf("successful current-series mapping was not reused: calls=%d cmd=%v state=%+v", service.wikipediaVolumeCalls, cmd != nil, current)
	}
	newSeriesURL := "https://fixture.invalid/manga/new-series/"
	service.chapters = []komiku.Chapter{{RawID: "2", Display: "2", Number: 2, URL: "new"}}
	discovering, discoverCmd := current.startDiscover(newSeriesURL)
	current = discovering.(model)
	current, _ = updateModel(t, current, discoverCmd())
	if current.groupLoaded || current.groupView || len(current.groupMappings) != 0 {
		t.Fatalf("new series retained the prior Wikipedia mapping: %+v", current)
	}
	current, cmd = updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	current, _ = updateModel(t, current, cmd())
	if service.wikipediaVolumeCalls != 3 || service.wikipediaVolumeSeries != newSeriesURL {
		t.Fatalf("new series did not get its own lookup: calls=%d series=%q", service.wikipediaVolumeCalls, service.wikipediaVolumeSeries)
	}

	staleService := &fakeBackend{wikipediaVolumes: []komiku.Volume{{Volume: 9, Start: 1, End: 1}}}
	stale := newModel(staleService, packer.Raw, true, nil, time.Now)
	stale.screen, stale.seriesURL, stale.chapters = chaptersScreen, "https://fixture.invalid/manga/stale/", []komiku.Chapter{chapter}
	stale, staleCmd := updateModel(t, stale, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	stale, _ = updateModel(t, stale, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	stale, _ = updateModel(t, stale, staleCmd())
	if staleService.wikipediaVolumeCalls != 1 {
		t.Fatalf("repeated v while loading made %d lookups, want 1", staleService.wikipediaVolumeCalls)
	}
	if stale.groupView || stale.groupLoaded || len(stale.groupMappings) != 0 {
		t.Fatalf("cancelled Wikipedia result overwrote flat view: %+v", stale)
	}
}
func TestWikipediaLoadIsCancelledWhenRangeOrDownloadSupersedesIt(t *testing.T) {
	chapter := komiku.Chapter{RawID: "1", Display: "1", Number: 1, URL: "one"}
	service := &fakeBackend{
		wikipediaVolumes: []komiku.Volume{{Volume: 1, Start: 1, End: 1}},
		batch:            &fakeBatch{events: make(chan cli.Event)},
	}

	rangeModel := newModel(service, packer.Raw, true, nil, time.Now)
	rangeModel.screen, rangeModel.seriesURL, rangeModel.chapters = chaptersScreen, "https://fixture.invalid/manga/series/", []komiku.Chapter{chapter}
	rangeModel, staleRangeCmd := updateModel(t, rangeModel, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	rangeModel, _ = updateModel(t, rangeModel, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	rangeModel, _ = updateModel(t, rangeModel, staleRangeCmd())
	if rangeModel.screen != rangeScreen || rangeModel.groupLoading || rangeModel.groupView || rangeModel.groupLoaded {
		t.Fatalf("stale Wikipedia load survived range workflow: %+v", rangeModel)
	}

	downloadModel := newModel(service, packer.Raw, true, nil, time.Now)
	downloadModel.screen, downloadModel.seriesURL, downloadModel.chapters = chaptersScreen, "https://fixture.invalid/manga/series/", []komiku.Chapter{chapter}
	downloadModel.selected[chapter.URL] = true
	downloadModel, staleDownloadCmd := updateModel(t, downloadModel, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	downloadModel, _ = updateModel(t, downloadModel, tea.KeyMsg{Type: tea.KeyEnter})
	downloadModel, _ = updateModel(t, downloadModel, staleDownloadCmd())
	if downloadModel.screen != downloadingScreen || downloadModel.groupLoading || downloadModel.groupView || downloadModel.groupLoaded {
		t.Fatalf("stale Wikipedia load survived download workflow: %+v", downloadModel)
	}
}

func TestWikipediaVolumeHeadersAreFocusableAggregateControls(t *testing.T) {
	chapters := []komiku.Chapter{
		{RawID: "1", Display: "1", Number: 1, URL: "one"},
		{RawID: "2", Display: "2", Number: 2, URL: "two"},
		{RawID: "3", Display: "3", Number: 3, URL: "extra"},
	}
	service := &fakeBackend{wikipediaVolumes: []komiku.Volume{{Volume: 1, Start: 1, End: 2}}}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.screen, current.seriesURL, current.chapters = chaptersScreen, "https://fixture.invalid/manga/series/", chapters
	current, cmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	current, _ = updateModel(t, current, cmd())

	if !strings.Contains(current.View(), "[ ] Volume 01  ch 1-2") {
		t.Fatalf("unchecked aggregate control missing:\n%s", current.View())
	}
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyUp})
	if !strings.Contains(current.View(), "> [ ] Volume 01  ch 1-2") {
		t.Fatalf("Up did not focus the preceding volume header:\n%s", current.View())
	}
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeySpace})
	if !current.selected[chapters[0].URL] || !current.selected[chapters[1].URL] || current.selected[chapters[2].URL] {
		t.Fatalf("aggregate Space selected wrong chapters: %+v", current.selected)
	}
	if !strings.Contains(current.View(), "> [x] Volume 01  ch 1-2") {
		t.Fatalf("full aggregate state missing:\n%s", current.View())
	}
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyDown})
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeySpace})
	if current.selected[chapters[0].URL] || !current.selected[chapters[1].URL] || !strings.Contains(current.View(), "[-] Volume 01  ch 1-2") {
		t.Fatalf("child toggle did not produce partial aggregate state: selected=%+v\n%s", current.selected, current.View())
	}
}

func TestWikipediaVolumeHeadersExcludeFractionalExtras(t *testing.T) {
	chapters := []komiku.Chapter{
		{RawID: "1", Display: "1", Number: 1, URL: "one"},
		{RawID: "2", Display: "2", Number: 2, URL: "two"},
		{RawID: "2.5", Display: "2.5", Number: 2.5, URL: "extra"},
	}
	service := &fakeBackend{wikipediaVolumes: []komiku.Volume{{Volume: 1, Start: 1, End: 2}}}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.screen, current.seriesURL, current.chapters = chaptersScreen, "https://fixture.invalid/manga/series/", chapters
	current, cmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	current, _ = updateModel(t, current, cmd())
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyUp})
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeySpace})
	if !current.selected["one"] || !current.selected["two"] || current.selected["extra"] {
		t.Fatalf("volume aggregate included fractional extra: %+v", current.selected)
	}
	if !strings.Contains(current.View(), "Unmapped / extras") || !strings.Contains(current.View(), "ch 2.5") {
		t.Fatalf("fractional extra was not rendered as unmapped:\n%s", current.View())
	}
}

func TestCompleteWikipediaHeaderSelectionEnablesDonePack(t *testing.T) {
	chapters := []komiku.Chapter{
		{RawID: "1", Display: "1", Number: 1, URL: "one"},
		{RawID: "2", Display: "2", Number: 2, URL: "two"},
	}
	seriesStore, err := store.Open(t.TempDir(), "series")
	if err != nil {
		t.Fatal(err)
	}
	dirs := make([]string, 0, len(chapters))
	for _, chapter := range chapters {
		dir, err := seriesStore.ChapterDir(chapter.Display, "", 1, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "001.jpg"), append([]byte{0xff, 0xd8}, make([]byte, 32)...), 0o644); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, dir)
	}
	results := []cli.Result{
		{Chapter: chapters[0], Status: cli.Done, Success: 1, Total: 1, SourceDir: dirs[0]},
		{Chapter: chapters[1], Status: cli.Done, Success: 1, Total: 1, SourceDir: dirs[1]},
	}
	events := make(chan cli.Event)
	close(events)
	service := &fakeBackend{
		wikipediaVolumes: []komiku.Volume{{Volume: 1, Start: 1, End: 2}},
		batch:            &fakeBatch{events: events, summary: cli.Summary{Results: results, Counts: map[cli.Status]int{cli.Done: 2}}},
		packStore:        seriesStore,
	}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.screen, current.seriesURL, current.chapters = chaptersScreen, "http://localhost/manga/series/", chapters
	current, cmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	current, _ = updateModel(t, current, cmd())
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyUp})
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeySpace})
	current, waitCmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyEnter})
	current, _ = updateModel(t, current, waitCmd())

	if len(service.startedJobs) != 2 || service.startedJobs[0].Volume != 1 || service.startedJobs[0].Flat ||
		service.startedJobs[1].Volume != 1 || service.startedJobs[1].Flat {
		t.Fatalf("complete Wikipedia selection started flat jobs: %+v", service.startedJobs)
	}
	if len(service.preparedMappings) != 1 || service.preparedMappings[0].Volume != 1 ||
		current.packPlan.DisabledReason != "" || len(current.packPlan.Volumes) != 1 {
		t.Fatalf("Done pack stayed disabled: mappings=%+v plan=%+v", service.preparedMappings, current.packPlan)
	}
	manifest, err := cli.LoadPackManifest(seriesStore.Dir())
	if err != nil || len(manifest.Mappings) != 1 || len(manifest.Chapters) != 2 || service.recordedProvenance != "wikipedia-display" {
		t.Fatalf("promotion manifest was not finalized: manifest=%+v provenance=%q err=%v", manifest, service.recordedProvenance, err)
	}
}

func TestIncompleteWikipediaSelectionsStayFlatAndExplicitFlatWins(t *testing.T) {
	chapters := []komiku.Chapter{
		{RawID: "1", Display: "1", Number: 1, URL: "one"},
		{RawID: "2", Display: "2", Number: 2, URL: "two"},
		{RawID: "2.5", Display: "2.5", Number: 2.5, URL: "extra"},
	}
	tests := []struct {
		name         string
		selected     map[string]bool
		explicitFlat bool
		wantReason   string
	}{
		{"partial", map[string]bool{"one": true}, false, "volume 01 requires selected chapter 2"},
		{"mixed extra", map[string]bool{"one": true, "two": true, "extra": true}, false, "selected chapter 2.5 is an extra or non-integer chapter"},
		{"explicit flat full volume", map[string]bool{"one": true, "two": true}, true, "explicit flat layout was selected with r"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeBackend{batch: &fakeBatch{events: make(chan cli.Event)}}
			current := newModel(service, packer.Raw, true, nil, time.Now)
			current.screen, current.seriesURL, current.chapters = chaptersScreen, "https://fixture.invalid/manga/series/", chapters
			current.selected = test.selected
			current.groupLoaded = true
			current.groupMappings = []komiku.Volume{{Volume: 1, Start: 1, End: 2}}
			current.explicitFlat = test.explicitFlat

			current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyEnter})
			if !current.flat || len(current.mappings) != 0 || len(service.startedJobs) != len(test.selected) {
				t.Fatalf("incomplete/explicit selection changed layout: flat=%v mappings=%+v jobs=%+v", current.flat, current.mappings, service.startedJobs)
			}
			for _, job := range service.startedJobs {
				if !job.Flat || job.Volume != 0 {
					t.Fatalf("flat selection gained logical assignment: %+v", service.startedJobs)
				}
			}
			if !strings.Contains(current.packUnavailableReason, test.wantReason) {
				t.Fatalf("pack reason=%q, want %q", current.packUnavailableReason, test.wantReason)
			}
		})
	}
}
func TestWikipediaVolumeNavigationUsesRenderedControlOrderAndSkipsExtrasHeading(t *testing.T) {
	chapters := []komiku.Chapter{
		{RawID: "1", Display: "1", Number: 1, URL: "one"},
		{RawID: "2", Display: "2", Number: 2, URL: "two"},
		{RawID: "3", Display: "3", Number: 3, URL: "three"},
		{RawID: "4", Display: "4", Number: 4, URL: "four"},
		{RawID: "5", Display: "5", Number: 5, URL: "extra"},
	}
	service := &fakeBackend{wikipediaVolumes: []komiku.Volume{{Volume: 1, Start: 1, End: 2}, {Volume: 2, Start: 3, End: 4}}}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.screen, current.seriesURL, current.chapters = chaptersScreen, "https://fixture.invalid/manga/series/", chapters
	current, cmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	current, _ = updateModel(t, current, cmd())

	expected := []groupFocus{
		{kind: groupFocusVolume, volume: 1},
		{kind: groupFocusChapter, chapterURL: "one"},
		{kind: groupFocusChapter, chapterURL: "two"},
		{kind: groupFocusVolume, volume: 2},
		{kind: groupFocusChapter, chapterURL: "three"},
		{kind: groupFocusChapter, chapterURL: "four"},
		{kind: groupFocusChapter, chapterURL: "extra"},
	}
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyUp})
	for index, want := range expected {
		if !sameGroupFocus(current.groupFocus, want) {
			t.Fatalf("control %d focus=%+v want=%+v\n%s", index, current.groupFocus, want, current.View())
		}
		if index+1 < len(expected) {
			current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyDown})
		}
	}
	if strings.Count(current.View(), "Unmapped / extras") != 1 {
		t.Fatalf("extras heading duplicated or omitted:\n%s", current.View())
	}
}

func TestWikipediaVolumeAggregateUsesFullDiscoveredMembershipUnderFilter(t *testing.T) {
	chapters := []komiku.Chapter{
		{RawID: "1", Display: "one", Number: 1, URL: "one"},
		{RawID: "2", Display: "two", Number: 2, URL: "two"},
		{RawID: "3", Display: "three", Number: 3, URL: "three"},
	}
	service := &fakeBackend{wikipediaVolumes: []komiku.Volume{{Volume: 1, Start: 1, End: 4}}}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.screen, current.seriesURL, current.chapters = chaptersScreen, "https://fixture.invalid/manga/series/", chapters
	current.selected["one"] = true
	current, cmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	current, _ = updateModel(t, current, cmd())
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("two")})
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyEnter})
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyUp})
	if !strings.Contains(current.View(), "> [-] Volume 01  ch 1-4") {
		t.Fatalf("hidden selected child did not produce partial state:\n%s", current.View())
	}
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeySpace})
	if !current.selected["one"] || !current.selected["two"] || !current.selected["three"] || current.selectedCount() != 3 {
		t.Fatalf("partial aggregate did not select every discovered child: %+v", current.selected)
	}
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if !current.selected["one"] || current.selected["two"] || !current.selected["three"] {
		t.Fatalf("visible-all changed hidden children: %+v", current.selected)
	}
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeySpace})
	if !current.selected["one"] || !current.selected["two"] || !current.selected["three"] {
		t.Fatalf("partial aggregate Space did not restore full selection: %+v", current.selected)
	}
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeySpace})
	if current.selectedCount() != 0 {
		t.Fatalf("full aggregate Space did not clear discovered children: %+v", current.selected)
	}
}

func TestWikipediaVolumeFocusHandoffAndAsyncInsertionUseChapterIdentity(t *testing.T) {
	chapters := []komiku.Chapter{
		{RawID: "1", Display: "1", Number: 1, URL: "one"},
		{RawID: "2", Display: "2", Number: 2, URL: "two"},
	}
	service := &fakeBackend{wikipediaVolumes: []komiku.Volume{{Volume: 1, Start: 1, End: 2}}}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.screen, current.seriesURL, current.chapters = chaptersScreen, "https://fixture.invalid/manga/series/", chapters
	current, cmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyDown})
	current, _ = updateModel(t, current, cmd())
	if !sameGroupFocus(current.groupFocus, groupFocus{kind: groupFocusChapter, chapterURL: "two"}) {
		t.Fatalf("async header insertion lost chapter identity: %+v", current.groupFocus)
	}
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyUp})
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyUp})
	if current.groupFocus.kind != groupFocusVolume {
		t.Fatalf("volume header not reached before flat handoff: %+v", current.groupFocus)
	}
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	if current.chapterCursor != 0 {
		t.Fatalf("flat handoff from header cursor=%d, want first actual chapter", current.chapterCursor)
	}
	current, cmd = updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	if cmd != nil || !sameGroupFocus(current.groupFocus, groupFocus{kind: groupFocusChapter, chapterURL: "one"}) {
		t.Fatalf("cached group-on did not preserve flat chapter focus: cmd=%v focus=%+v", cmd != nil, current.groupFocus)
	}
}

func TestWikipediaVolumeAggregateRespectsMappedEligibilityAndDownloadInvariants(t *testing.T) {
	chapters := []komiku.Chapter{
		{RawID: "1", Display: "1", Number: 1, URL: "one"},
		{RawID: "2", Display: "2", Number: 2, URL: "two"},
	}
	service := &fakeBackend{wikipediaVolumes: []komiku.Volume{{Volume: 1, Start: 1, End: 2}}}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.screen, current.seriesURL, current.chapters = chaptersScreen, "https://fixture.invalid/manga/series/", chapters
	current.flat = false
	current.selected["two"] = true
	current.assignments["two"] = 9
	current.mappings = []komiku.Volume{{Volume: 9, Start: 2, End: 2}}
	beforeJobs := current.selectedJobs()
	current, cmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	current, _ = updateModel(t, current, cmd())
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyUp})
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeySpace})
	afterJobs := current.selectedJobs()
	if current.selected["one"] || !current.selected["two"] || service.saveCalls != 0 || current.flat || !strings.Contains(current.message, "outside the selected download mappings") {
		t.Fatalf("aggregate bypassed mapped eligibility: selected=%+v state=%+v", current.selected, current)
	}
	if len(beforeJobs) != 1 || len(afterJobs) != 1 || beforeJobs[0].Chapter.URL != afterJobs[0].Chapter.URL || beforeJobs[0].Volume != afterJobs[0].Volume {
		t.Fatalf("aggregate changed download jobs: before=%+v after=%+v", beforeJobs, afterJobs)
	}

	current.selected["one"] = true
	current.jobs = []cli.Job{{Chapter: chapters[1], Volume: 9}}
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeySpace})
	if current.selectedCount() != 0 {
		t.Fatalf("full mapped aggregate with an unassigned member did not clear atomically: %+v", current.selected)
	}
	if current.flat || len(current.mappings) != 1 || current.mappings[0].Volume != 9 ||
		len(current.assignments) != 1 || current.assignments["two"] != 9 || service.saveCalls != 0 {
		t.Fatalf("full aggregate clear changed mapped layout state: %+v", current)
	}
	if len(current.jobs) != 1 || current.jobs[0].Chapter.URL != "two" || current.jobs[0].Volume != 9 ||
		len(current.selectedJobs()) != 0 {
		t.Fatalf("full aggregate clear changed stored jobs or retained selected jobs: stored=%+v selected=%+v", current.jobs, current.selectedJobs())
	}
}

func TestWikipediaVolumeEmptyMappingKeepsFlatViewAndCanRetry(t *testing.T) {
	service := &fakeBackend{}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.screen, current.seriesURL = chaptersScreen, "https://fixture.invalid/manga/series/"
	current.chapters = []komiku.Chapter{{RawID: "1", Display: "1", Number: 1, URL: "one"}}
	current, cmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	current, _ = updateModel(t, current, cmd())
	if current.groupView || !current.groupEmpty || !strings.Contains(current.View(), "[NO MAP] Wikipedia has no usable volume grouping.") {
		t.Fatalf("empty mapping state is not recoverable: %+v\n%s", current, current.View())
	}
	service.wikipediaVolumes = []komiku.Volume{{Volume: 1, Start: 1, End: 1}}
	current, cmd = updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	if cmd == nil {
		t.Fatal("v did not retry an empty mapping")
	}
}

func TestModelURLFallbackAndRecoverableEmptyAndErrorStates(t *testing.T) {
	seriesURL := "https://fixture.invalid/manga/actual-series/"
	chapter := komiku.Chapter{RawID: "271-5", Display: "271.5", Number: 271.5, URL: "https://fixture.invalid/actual-series-chapter-271-5/"}
	service := &fakeBackend{chapters: []komiku.Chapter{chapter}}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.input.SetValue(seriesURL)
	current, cmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyEnter})
	if !current.loading || current.loadingLabel != "Loading chapters" {
		t.Fatalf("URL loading state=%+v", current)
	}
	current, _ = updateModel(t, current, cmd())
	if service.searchQuery != "" || service.discoverURL != seriesURL || current.screen != chaptersScreen {
		t.Fatalf("URL fallback searched=%q discovered=%q screen=%v", service.searchQuery, service.discoverURL, current.screen)
	}

	emptyService := &fakeBackend{}
	current = newModel(emptyService, packer.Raw, true, nil, time.Now)
	current.input.SetValue("missing")
	current, cmd = updateModel(t, current, tea.KeyMsg{Type: tea.KeyEnter})
	current, _ = updateModel(t, current, cmd())
	if current.emptyLabel == "" || !current.input.Focused() || current.input.Value() != "missing" {
		t.Fatalf("empty state is not recoverable: %+v", current)
	}

	errorService := &fakeBackend{searchErr: errors.New("fixture markup changed")}
	current = newModel(errorService, packer.Raw, true, nil, time.Now)
	current.input.SetValue("broken")
	current, cmd = updateModel(t, current, tea.KeyMsg{Type: tea.KeyEnter})
	current, _ = updateModel(t, current, cmd())
	if current.err == nil || !current.input.Focused() || current.input.Value() != "broken" {
		t.Fatalf("error state is not recoverable: %+v", current)
	}

	emptyDiscover := &fakeBackend{}
	current = newModel(emptyDiscover, packer.Raw, true, nil, time.Now)
	current.input.SetValue(seriesURL)
	current, cmd = updateModel(t, current, tea.KeyMsg{Type: tea.KeyEnter})
	current, _ = updateModel(t, current, cmd())
	if current.emptyLabel == "" || current.screen != searchScreen {
		t.Fatalf("empty discovery state=%+v", current)
	}
}

func TestModelInvalidPastedSeriesURLIsRecoverableDiscoveryError(t *testing.T) {
	seriesURL := "https://attacker.invalid/manga/series/"
	_, validationErr := komiku.ValidateSeriesURL(seriesURL)
	if validationErr == nil {
		t.Fatal("test URL unexpectedly passed canonical validation")
	}
	service := &fakeBackend{discoverErr: validationErr}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.input.SetValue(seriesURL)

	current, cmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyEnter})
	if !current.loading || cmd == nil {
		t.Fatalf("invalid pasted URL did not enter ordinary discovery loading state: %+v", current)
	}
	current, _ = updateModel(t, current, cmd())
	if current.screen != searchScreen || current.err == nil || !current.input.Focused() || current.input.Value() != seriesURL {
		t.Fatalf("invalid pasted URL is not a recoverable discovery error: %+v", current)
	}
	if service.discoverURL != seriesURL || service.searchQuery != "" {
		t.Fatalf("invalid pasted URL bypassed discovery path: discover=%q search=%q", service.discoverURL, service.searchQuery)
	}
}

func TestModelPauseResumeAndSafeStopWaitsForBatchClose(t *testing.T) {
	chapter := komiku.Chapter{RawID: "1", Display: "1", Number: 1, URL: "https://fixture.invalid/chapter/one/"}
	batch := &fakeBatch{
		events:  make(chan cli.Event, 4),
		summary: cli.Summary{Results: []cli.Result{{Chapter: chapter, Status: cli.Part}}, Counts: map[cli.Status]int{cli.Part: 1}, Requested: 1, Started: 1, Cancelled: true},
	}
	service := &fakeBackend{batch: batch, packPlan: cli.PackPlan{DisabledReason: "Only mapped volume runs can be packed."}}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.screen, current.seriesURL, current.chapters = chaptersScreen, "https://fixture.invalid/manga/series/", []komiku.Chapter{chapter}
	current.selected[chapter.URL] = true
	current, waitCmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyEnter})
	if current.screen != downloadingScreen || len(service.startedJobs) != 1 {
		t.Fatalf("download did not start: screen=%v jobs=%+v", current.screen, service.startedJobs)
	}
	batch.events <- cli.Event{Kind: cli.BatchStarted, At: time.Unix(10, 0)}
	current, waitCmd = updateModel(t, current, waitCmd())
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeySpace})
	if batch.pauseCalls != 1 || !current.paused {
		t.Fatalf("pause calls=%d state=%+v", batch.pauseCalls, current)
	}
	batch.events <- cli.Event{Kind: cli.BatchPaused, At: time.Unix(11, 0)}
	current, waitCmd = updateModel(t, current, waitCmd())
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeySpace})
	if batch.resumeCalls != 1 || !current.resuming {
		t.Fatalf("resume calls=%d state=%+v", batch.resumeCalls, current)
	}
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if batch.cancelCalls != 1 || !current.stopping {
		t.Fatalf("cancel calls=%d state=%+v", batch.cancelCalls, current)
	}
	close(batch.events)
	current, quitCmd := updateModel(t, current, waitCmd())
	if current.batch != nil || quitCmd == nil {
		t.Fatalf("batch did not close before quit: state=%+v", current)
	}
	if _, ok := quitCmd().(tea.QuitMsg); !ok {
		t.Fatalf("close command was not tea.Quit")
	}
}

func TestModelProgressMetricsTailAndPackLifecycle(t *testing.T) {
	now := time.Unix(20, 0)
	chapter := komiku.Chapter{RawID: "1", Display: "1", Number: 1, URL: "https://fixture.invalid/chapter/one/"}
	service := &fakeBackend{
		packPlan:     cli.PackPlan{Preset: packer.Raw, Volumes: []packer.Volume{{Series: "series", Number: 1}}},
		packOutcomes: []cli.PackOutcome{{Volume: 1, Result: packer.Result{Path: "series Volume 01.cbz", Preset: packer.Raw}}},
	}
	current := newModel(service, packer.Raw, true, nil, func() time.Time { return now })
	current.jobs = []cli.Job{{Chapter: chapter}, {Chapter: chapter}}
	current.startedAt = time.Unix(10, 0)
	current.completed = 0
	current.bytes = 10 * 1024
	current.applyEngineEvent(cli.Event{Kind: cli.PageFailed, Chapter: chapter, Page: 2, Pages: 3, Err: errors.New("fixture failure")})
	current.applyEngineEvent(cli.Event{Kind: cli.ChapterFinished, Chapter: chapter, Result: cli.Result{Chapter: chapter, Status: cli.Part, Success: 2, Total: 3}})
	if current.speed() != "1.0 KiB/s" || current.eta() != "10s" || current.errorCount != 1 {
		t.Fatalf("speed=%q eta=%q errors=%d", current.speed(), current.eta(), current.errorCount)
	}
	if len(current.tail) != 2 || current.tail[0][:6] != "[FAIL]" || current.tail[1][:6] != "[PART]" {
		t.Fatalf("tail=%+v", current.tail)
	}

	current.screen = doneScreen
	current.packPlan = service.packPlan
	current, cmd := updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if current.screen != packingScreen || cmd == nil {
		t.Fatalf("pack did not start: %+v", current)
	}
	current, _ = updateModel(t, current, cmd())
	if current.screen != doneScreen || len(current.packOutcomes) != 1 || current.packOutcomes[0].Result.Preset != packer.Raw {
		t.Fatalf("pack outcome=%+v", current)
	}

	cancelled := false
	current.screen = packingScreen
	current.packCancel = func() { cancelled = true }
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if !cancelled || !current.stopping {
		t.Fatalf("pack cancellation state=%+v cancelled=%v", current, cancelled)
	}
}

func TestModelFatalSummaryAndUniqueResultErrors(t *testing.T) {
	chapterOne := komiku.Chapter{RawID: "1", Display: "1", Number: 1, URL: "one"}
	chapterTwo := komiku.Chapter{RawID: "2", Display: "2", Number: 2, URL: "two"}
	service := &fakeBackend{packPlan: cli.PackPlan{DisabledReason: "not packable"}}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.applyEngineEvent(cli.Event{Kind: cli.PageFailed, Chapter: chapterOne, Page: 2, Pages: 3, Err: errors.New("image failed")})
	current.applyEngineEvent(cli.Event{Kind: cli.ChapterFinished, Chapter: chapterOne, Result: cli.Result{
		Chapter: chapterOne,
		Status:  cli.Part,
		Errors:  []string{"page 002: image failed", "chapter state failed"},
	}})
	if current.errorCount != 2 {
		t.Fatalf("live errors=%d", current.errorCount)
	}
	summary := cli.Summary{
		Results: []cli.Result{
			{Chapter: chapterOne, Status: cli.Part, Errors: []string{"page 002: image failed", "chapter state failed"}},
			{Chapter: chapterTwo, Status: cli.Fail, Errors: []string{"chapter state failed"}},
		},
		Counts: map[cli.Status]int{cli.Part: 1, cli.Fail: 1},
		Err:    errors.New("sync audit: disk full"),
	}
	current, _ = updateModel(t, current, batchClosedMsg{summary: summary})
	if current.errorCount != 3 || current.err == nil || current.message != "Download data finished, but finalization failed." {
		t.Fatalf("final state errors=%d err=%v message=%q", current.errorCount, current.err, current.message)
	}
	view := current.View()
	if !strings.Contains(view, "[ERROR] Finalize download: sync audit: disk full") || !strings.Contains(view, "Errors 3") {
		t.Fatalf("fatal summary view=%q", view)
	}
	current.applyEngineEvent(cli.Event{Kind: cli.BatchFinished})
	if current.message != "Download and audit finished." {
		t.Fatalf("finished copy=%q", current.message)
	}
}

func TestManualRangeInvalidVolumeSelectionDoesNotPersist(t *testing.T) {
	chapters := []komiku.Chapter{
		{RawID: "1", Display: "1", Number: 1, URL: "one"},
		{RawID: "2", Display: "2", Number: 2, URL: "two"},
	}
	service := &fakeBackend{}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.screen, current.seriesURL, current.chapters = rangeScreen, "https://fixture.invalid/manga/series/", chapters
	current.rangeMode = manualRange
	current.rangeInput.SetValue("1:1-2 | 9")
	updated, cmd := current.applyRange()
	current = updated.(model)
	if cmd != nil || current.err == nil || service.saveCalls != 0 || len(service.savedVolumes) != 0 {
		t.Fatalf("invalid RHS persisted: cmd=%v err=%v saves=%d volumes=%+v", cmd != nil, current.err, service.saveCalls, service.savedVolumes)
	}
}

func TestMappedSelectionPreservesMappingsAndLimitsToggles(t *testing.T) {
	chapters := []komiku.Chapter{
		{RawID: "1", Display: "1", Number: 1, URL: "one"},
		{RawID: "2", Display: "2", Number: 2, URL: "two"},
		{RawID: "3", Display: "3", Number: 3, URL: "unmapped"},
	}
	current := newModel(&fakeBackend{}, packer.Raw, true, nil, time.Now)
	current.screen, current.chapters = chaptersScreen, chapters
	current.applyVolumeJobs([]cli.Job{{Chapter: chapters[0], Volume: 1}, {Chapter: chapters[1], Volume: 1}}, []komiku.Volume{{Volume: 1, Start: 1, End: 2}})

	current.chapterCursor = 2
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeySpace})
	if current.selected[chapters[2].URL] || current.flat || !strings.Contains(current.message, "outside the selected volume mappings") {
		t.Fatalf("unmapped toggle changed state: %+v", current)
	}

	current.chapterCursor = 0
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeySpace})
	if current.selected[chapters[0].URL] || current.flat || len(current.mappings) != 1 || current.assignments[chapters[0].URL] != 1 {
		t.Fatalf("mapped toggle destroyed mapping: %+v", current)
	}
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if !current.selected[chapters[0].URL] || !current.selected[chapters[1].URL] || current.selected[chapters[2].URL] || current.flat {
		t.Fatalf("mapped visible-all selected wrong chapters: %+v", current.selected)
	}
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if current.selected[chapters[0].URL] || current.selected[chapters[1].URL] || current.flat {
		t.Fatalf("mapped visible-all did not toggle covered set: %+v", current.selected)
	}
	current.screen = rangeScreen
	current.rangeMode = chapterRange
	current.rangeInput.SetValue("1")
	updated, _ := current.applyRange()
	current = updated.(model)
	if current.flat || !current.selected[chapters[0].URL] || len(current.mappings) != 1 {
		t.Fatalf("chapter range silently switched mapped selection to flat: %+v", current)
	}
}

func TestVisibleAllOnlyTogglesFilteredItemsAndNoSelectionDoesNotStart(t *testing.T) {
	chapters := []komiku.Chapter{
		{RawID: "1", Display: "one", Number: 1, URL: "one"},
		{RawID: "2", Display: "two", Number: 2, URL: "two"},
	}
	service := &fakeBackend{}
	current := newModel(service, packer.Raw, true, nil, time.Now)
	current.screen, current.chapters = chaptersScreen, chapters
	current.selected[chapters[1].URL] = true
	current.filter.SetValue("one")
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if !current.selected[chapters[0].URL] || !current.selected[chapters[1].URL] {
		t.Fatalf("hidden selection changed: %+v", current.selected)
	}
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if current.selected[chapters[0].URL] || !current.selected[chapters[1].URL] {
		t.Fatalf("visible toggle changed hidden selection: %+v", current.selected)
	}
	current.selected[chapters[1].URL] = false
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyEnter})
	if service.startCalls != 0 || len(service.startedJobs) != 0 || !strings.Contains(current.message, "Select at least one") {
		t.Fatalf("empty selection started backend: calls=%d jobs=%+v message=%q", service.startCalls, service.startedJobs, current.message)
	}
}

func TestStaleSearchDiscoveryAndVolumeMessagesAreIgnored(t *testing.T) {
	service := &fakeBackend{}
	current := newModel(service, packer.Raw, true, nil, time.Now)

	current.loading = true
	current.operationID = 10
	current.opCancel = func() {}
	current, _ = updateModel(t, current, tea.KeyMsg{Type: tea.KeyEsc})
	current.searchResults = []komiku.Series{{Title: "new"}}
	current, _ = updateModel(t, current, searchMsg{id: 10, results: []komiku.Series{{Title: "old"}}})
	if len(current.searchResults) != 1 || current.searchResults[0].Title != "new" {
		t.Fatalf("stale search overwrote state: %+v", current.searchResults)
	}

	current.operationID = 20
	current.screen = chaptersScreen
	current.seriesURL = "new-series"
	current.chapters = []komiku.Chapter{{RawID: "2", URL: "new"}}
	current, _ = updateModel(t, current, discoverMsg{id: 19, seriesURL: "old-series", chapters: []komiku.Chapter{{RawID: "1", URL: "old"}}})
	if current.seriesURL != "new-series" || current.chapters[0].RawID != "2" {
		t.Fatalf("stale discovery overwrote state: url=%q chapters=%+v", current.seriesURL, current.chapters)
	}

	current.operationID = 30
	current.flat = true
	current, _ = updateModel(t, current, volumeSelectionMsg{id: 29, volumes: []komiku.Volume{{Volume: 1}}, jobs: []cli.Job{{Chapter: current.chapters[0], Volume: 1}}})
	if !current.flat || len(current.mappings) != 0 {
		t.Fatalf("stale volume selection overwrote state: %+v", current)
	}
}

func TestRichAsyncOperationsRestartSpinner(t *testing.T) {
	service := &fakeBackend{volumes: []komiku.Volume{{Volume: 1, Start: 1, End: 1}}}
	current := newModel(service, packer.Raw, false, nil, time.Now)
	_, cmd := current.startSearch("query")
	message := cmd()
	if batch, ok := message.(tea.BatchMsg); !ok || len(batch) != 2 {
		t.Fatalf("search did not restart spinner: %T", message)
	}
	_, cmd = current.startDiscover("https://fixture.invalid/manga/series/")
	message = cmd()
	if batch, ok := message.(tea.BatchMsg); !ok || len(batch) != 2 {
		t.Fatalf("discover did not restart spinner: %T", message)
	}
	current.screen = rangeScreen
	current.rangeMode = volumeRange
	current.chapters = []komiku.Chapter{{RawID: "1", Display: "1", Number: 1, URL: "one"}}
	current.rangeInput.SetValue("1")
	ranged, cmd := current.applyRange()
	current = ranged.(model)
	message = cmd()
	if batch, ok := message.(tea.BatchMsg); !ok || len(batch) != 2 {
		t.Fatalf("volume range did not restart spinner: %T", message)
	}
	current.rangeLoading = true
	tick := current.spinner.Tick()
	_, next := current.Update(tick)
	if next == nil {
		t.Fatal("volume spinner tick did not reschedule")
	}
}
