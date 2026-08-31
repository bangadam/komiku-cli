package tui

import (
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/bangadam/komiku-cli/cli"
	"github.com/bangadam/komiku-cli/komiku"
	packer "github.com/bangadam/komiku-cli/pack"
)

func TestPlainViewContainsOnlyASCIIWithoutANSI(t *testing.T) {
	current := newModel(&fakeBackend{}, packer.Raw, true, nil, time.Now)
	current.width = 80
	current.screen = doneScreen
	current.summary = cli.Summary{
		Counts:      map[cli.Status]int{cli.Done: 1, cli.Part: 1},
		PagesOK:     2,
		PagesFailed: 1,
		OutputDir:   "output/日本語",
		AuditPath:   "audit/run.log",
	}
	current.packPlan = cli.PackPlan{DisabledReason: "Only mapped volumes can be packed."}
	view := current.View()
	if strings.Contains(view, "\x1b[") {
		t.Fatalf("plain view contains ANSI: %q", view)
	}
	for len(view) > 0 {
		r, size := utf8.DecodeRuneInString(view)
		if r > 127 {
			t.Fatalf("plain view contains non-ASCII %q in %q", r, view)
		}
		view = view[size:]
	}
}

func TestRichFocusUsesReverseVideoWithoutFixedColors(t *testing.T) {
	renderer := lipgloss.NewRenderer(io.Discard)
	renderer.SetColorProfile(termenv.ANSI)
	current := newModel(&fakeBackend{}, packer.Raw, false, renderer, time.Now)
	current.width = 80
	current.screen = chaptersScreen
	current.chapters = []komiku.Chapter{{RawID: "1", Display: "1", Number: 1, URL: "chapter-one"}}

	focusView := current.View()
	reverseVideo := regexp.MustCompile(`\x1b\[(?:[0-9]+;)*7(?:;[0-9]+)*m`)
	if !reverseVideo.MatchString(focusView) {
		t.Fatalf("rich focus lacks reverse-video emphasis: %q", focusView)
	}
	fixedColor := regexp.MustCompile(`\x1b\[[0-9;]*(?:3[0-9]|4[0-9]|9[0-9]|10[0-7])(?:;[0-9;]*)?m`)
	if fixedColor.MatchString(focusView) {
		t.Fatalf("rich focus contains a fixed foreground/background color: %q", focusView)
	}
	current.groupView, current.groupLoaded = true, true
	current.groupMappings = []komiku.Volume{{Volume: 1, Start: 1, End: 1}}
	current.groupFocus = groupFocus{kind: groupFocusVolume, volume: 1}
	volumeFocusView := current.View()
	bold := regexp.MustCompile(`\x1b\[(?:[0-9]+;)*1(?:;[0-9]+)*m`)
	if !strings.Contains(volumeFocusView, "Volume 01") || !reverseVideo.MatchString(volumeFocusView) || !bold.MatchString(volumeFocusView) {
		t.Fatalf("rich volume focus lacks bold reverse-video emphasis: %q", volumeFocusView)
	}
	if fixedColor.MatchString(volumeFocusView) {
		t.Fatalf("rich volume focus contains a fixed foreground/background color: %q", volumeFocusView)
	}

	current.screen = downloadingScreen
	current.jobs = []cli.Job{{Chapter: current.chapters[0]}}
	if progressView := current.View(); fixedColor.MatchString(progressView) {
		t.Fatalf("rich progress contains a fixed foreground/background color: %q", progressView)
	}
}

func TestViewsFitSupportedTerminalWidths(t *testing.T) {
	renderer := lipgloss.NewRenderer(io.Discard)
	chapter := komiku.Chapter{RawID: "271.5-extra-long-raw-identity", Display: "271.5 extra long display label", Number: 271.5, URL: "https://fixture.invalid/chapter/non-derived-identity/"}
	for _, width := range []int{20, 40, 60, 80, 100, 120} {
		base := newModel(&fakeBackend{}, packer.Raw, false, renderer, time.Now)
		base.width, base.height = width, 24
		base.input.SetValue("an intentionally long series query that remains editable")
		base.searchResults = []komiku.Series{{Title: strings.Repeat("Long exact series title ", 6), Slug: "actual-slug", URL: "https://fixture.invalid/manga/actual/"}}
		base.chapters = []komiku.Chapter{chapter}
		base.seriesURL = "https://fixture.invalid/manga/actual-series-with-a-long-path/"
		base.selected[chapter.URL] = true
		base.jobs = []cli.Job{{Chapter: chapter, Flat: true}}
		base.latest, base.latestPage, base.latestPages = chapter, 12, 50
		base.startedAt, base.bytes = time.Now().Add(-time.Second), 4096
		base.summary = cli.Summary{Counts: map[cli.Status]int{cli.Done: 1}, PagesOK: 50, OutputDir: strings.Repeat("output/", 15), AuditPath: strings.Repeat("audit/", 15)}
		base.packPlan = cli.PackPlan{Preset: packer.Raw, DisabledReason: "Only mapped volumes can be packed."}

		base.groupView, base.groupLoaded = true, true
		base.groupMappings = []komiku.Volume{{Volume: 28, Start: 246, End: 255}}
		base.outputRoot = strings.Repeat("long-output/", 15)
		base.packSeries = []string{strings.Repeat("long-series/", 15)}
		base.packSeriesDir = strings.Repeat("long-series/", 15)
		base.standalonePackOutput = strings.Repeat("packed output/", 15)
		for _, target := range []screen{homeScreen, outputScreen, searchScreen, chaptersScreen, rangeScreen, downloadingScreen, doneScreen, packingScreen, packSeriesScreen, packPathScreen, packRecoveryScreen, standalonePackingScreen, standalonePackDoneScreen} {
			base.screen = target
			for lineNumber, line := range strings.Split(base.View(), "\n") {
				if got := ansi.StringWidth(line); got > width {
					t.Fatalf("screen=%v width=%d line=%d rendered=%d: %q", target, width, lineNumber, got, line)
				}
			}
		}
	}
}

func TestWikipediaGroupedViewShowsExactBoundariesAndPagesByChapter(t *testing.T) {
	chapters := []komiku.Chapter{
		{RawID: "1", Display: "1", Number: 1, URL: "one"},
		{RawID: "2", Display: "2", Number: 2, URL: "two"},
		{RawID: "3", Display: "3", Number: 3, URL: "three"},
		{RawID: "4", Display: "4", Number: 4, URL: "four"},
		{RawID: "5", Display: "5", Number: 5, URL: "five"},
	}
	current := newModel(&fakeBackend{}, packer.Raw, true, nil, time.Now)
	current.width, current.height = 80, 24
	current.screen, current.chapters = chaptersScreen, chapters
	current.groupView, current.groupLoaded = true, true
	current.groupMappings = []komiku.Volume{{Volume: 1, Start: 1, End: 2}, {Volume: 2, Start: 3, End: 4}}

	view := current.View()
	for _, wanted := range []string{
		"[ ] Volume 01  ch 1-2",
		"[ ] Volume 02  ch 3-4",
		"Unmapped / extras",
		"Space toggle chapter/volume",
		"Volume view source: Wikipedia",
		"v flat",
	} {
		if !strings.Contains(view, wanted) {
			t.Fatalf("missing %q in grouped view:\n%s", wanted, view)
		}
	}

	current.height = 13
	current.chapterCursor = 4
	view = current.View()
	if !strings.Contains(view, "Unmapped / extras") || !strings.Contains(view, ">   [ ] ch 5") {
		t.Fatalf("paged view lost the focused chapter or its extras heading:\n%s", view)
	}
}
func TestGroupedPagingKeepsCanonicalFocusedControlVisibleAtShortHeights(t *testing.T) {
	chapters := []komiku.Chapter{
		{RawID: "1", Display: "1", Number: 1, URL: "one"},
		{RawID: "2", Display: "2", Number: 2, URL: "two"},
		{RawID: "3", Display: "3", Number: 3, URL: "three"},
		{RawID: "4", Display: "4", Number: 4, URL: "four"},
		{RawID: "5", Display: "5", Number: 5, URL: "extra"},
	}
	current := newModel(&fakeBackend{}, packer.Raw, true, nil, time.Now)
	current.width, current.height = 80, 8
	current.screen, current.chapters = chaptersScreen, chapters
	current.groupView, current.groupLoaded = true, true
	current.groupMappings = []komiku.Volume{{Volume: 1, Start: 1, End: 2}, {Volume: 2, Start: 3, End: 4}}

	tests := []struct {
		focus  groupFocus
		wanted string
	}{
		{groupFocus{kind: groupFocusVolume, volume: 1}, "> [ ] Volume 01"},
		{groupFocus{kind: groupFocusChapter, chapterURL: "two"}, ">   [ ] ch 2"},
		{groupFocus{kind: groupFocusVolume, volume: 2}, "> [ ] Volume 02"},
		{groupFocus{kind: groupFocusChapter, chapterURL: "extra"}, ">   [ ] ch 5"},
	}
	for _, test := range tests {
		current.groupFocus = test.focus
		view := current.View()
		if !strings.Contains(view, test.wanted) {
			t.Fatalf("focused control %q is outside short page:\n%s", test.wanted, view)
		}
		if lines := strings.Count(view, "\n") + 1; lines > current.height {
			t.Fatalf("short view has %d lines, height %d:\n%s", lines, current.height, view)
		}
		if strings.Count(view, "Volume 01") > 1 || strings.Count(view, "Volume 02") > 1 {
			t.Fatalf("page duplicated a canonical volume header:\n%s", view)
		}
	}
	current.groupFocus = groupFocus{kind: groupFocusVolume, volume: 2}
	updated, _ := current.Update(tea.WindowSizeMsg{Width: 20, Height: 8})
	resizedModel := updated.(model)
	resized := resizedModel.View()
	if !sameGroupFocus(resizedModel.groupFocus, current.groupFocus) || !strings.Contains(resized, "> [ ] Volume") {
		t.Fatalf("resize lost the focused volume control:\n%s", resized)
	}
	for lineNumber, line := range strings.Split(resized, "\n") {
		if got := ansi.StringWidth(line); got > 20 {
			t.Fatalf("resized grouped line %d rendered width %d: %q", lineNumber, got, line)
		}
	}
}

func TestMappedCoverageAndPerVolumePackOutcomesAreVisible(t *testing.T) {
	current := newModel(&fakeBackend{}, packer.Raw, true, nil, time.Now)
	current.width = 100
	current.screen = chaptersScreen
	current.flat = false
	current.mappings = []komiku.Volume{{Volume: 1, Start: 1, End: 7}, {Volume: 2, Start: 8, End: 8}}
	view := current.View()
	if !strings.Contains(view, "Volume 01: ch 1-7") || !strings.Contains(view, "Volume 02: ch 8") {
		t.Fatalf("mapped coverage missing: %q", view)
	}

	current.screen = doneScreen
	current.summary = cli.Summary{Counts: map[cli.Status]int{}}
	current.packPlan = cli.PackPlan{DisabledReason: "already attempted"}
	current.packOutcomes = []cli.PackOutcome{
		{Volume: 1, Result: packer.Result{Path: "one.cbz", Preset: packer.Raw}},
		{Volume: 2, Result: packer.Result{Path: "two.cbz", Preset: packer.Medium, Warnings: []packer.Warning{{Entry: "page", Source: "source", Err: errors.New("decode")}}}},
		{Volume: 3, Err: errors.New("missing page")},
	}
	view = current.View()
	for _, wanted := range []string{"[PACKED] Volume 01: one.cbz preset=raw", "[WARNING] Volume 02:", "[PACK FAILED] Volume 03: missing page"} {
		if !strings.Contains(view, wanted) {
			t.Fatalf("missing %q in %q", wanted, view)
		}
	}
}

func TestStandalonePackDoneViewKeepsEveryOutputLine(t *testing.T) {
	current := newModel(&fakeBackend{}, packer.Raw, true, nil, time.Now)
	current.screen = standalonePackDoneScreen
	current.packSeriesDir = "/manga/series"
	current.standalonePackOutput = "created: Volume 01.cbz\ncreated: Volume 02.cbz\nrecovered manifest: /manga/series/.pack.json"
	view := current.View()
	for _, want := range strings.Split(current.standalonePackOutput, "\n") {
		if !strings.Contains(view, want) {
			t.Fatalf("missing result line %q:\n%s", want, view)
		}
	}
}

func TestSearchViewShowsOutputRootBeforeDownload(t *testing.T) {
	current := newModel(&fakeBackend{}, packer.Raw, true, nil, time.Now)
	current.outputRoot = "/tmp/manga"
	if got := current.View(); !strings.Contains(got, "Save to: /tmp/manga") {
		t.Fatalf("view=%q", got)
	}
}
