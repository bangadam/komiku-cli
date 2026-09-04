package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bangadam/komiku-cli/store"
)

const verifyCommandUsage = "usage: komiku-cli verify <series-dir> [--json]"

// VerifyChapter is the verification outcome of one chapter directory.
type VerifyChapter struct {
	Dir          string   `json:"dir"`
	Number       float64  `json:"number"`
	Chapter      string   `json:"chapter"`
	Pages        int      `json:"pages"`
	ValidPages   int      `json:"valid_pages"`
	BrokenPages  []string `json:"broken_pages,omitempty"`
	StrayFiles   []string `json:"stray_files,omitempty"`
	MissingPages []int    `json:"missing_pages,omitempty"`
}

// VerifyReport is the offline integrity report of one series directory.
type VerifyReport struct {
	SeriesDir    string          `json:"series_dir"`
	Chapters     []VerifyChapter `json:"chapters"`
	DoneCount    int             `json:"done_count"`
	NotInState   []string        `json:"chapters_not_in_state,omitempty"`
	Healthy      bool            `json:"healthy"`
	Problems     int             `json:"problems"`
}

func NewVerifyCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "verify <series-dir>",
		Short: "Check downloaded pages offline for valid images and gaps",
		Args:  exactOneArg(verifyCommandUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVerify(args[0], asJSON, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the verify report as JSON")
	return cmd
}

func runVerify(seriesDir string, asJSON bool, stdout io.Writer) error {
	if strings.TrimSpace(seriesDir) == "" {
		return errors.New("series directory is empty")
	}
	report, err := verifySeriesDir(seriesDir)
	if err != nil {
		return err
	}
	if asJSON {
		encoder := json.NewEncoder(stdout)
		return encoder.Encode(report)
	}
	fmt.Fprintf(stdout, "%s  chapters=%d done=%d problems=%d\n", report.SeriesDir, len(report.Chapters), report.DoneCount, report.Problems)
	for _, chapter := range report.Chapters {
		status := "OK"
		if len(chapter.BrokenPages) > 0 || len(chapter.MissingPages) > 0 {
			status = "BROKEN"
		}
		fmt.Fprintf(stdout, "%s %s pages=%d/%d\n", status, chapter.Dir, chapter.ValidPages, chapter.Pages)
		for _, page := range chapter.BrokenPages {
			fmt.Fprintf(stdout, "  broken: %s\n", page)
		}
		for _, page := range chapter.MissingPages {
			fmt.Fprintf(stdout, "  missing: page %03d\n", page)
		}
		for _, stray := range chapter.StrayFiles {
			fmt.Fprintf(stdout, "  stray: %s\n", stray)
		}
	}
	for _, dir := range report.NotInState {
		fmt.Fprintf(stdout, "not in state: %s\n", dir)
	}
	if report.Problems == 0 {
		fmt.Fprintln(stdout, "all chapters verified")
	}
	return nil
}

// verifySeriesDir walks the chapter directories of a series download and
// validates every page image offline: magic bytes, page numbering gaps, and
// stray non-image files. No network requests are made.
func verifySeriesDir(seriesDir string) (VerifyReport, error) {
	report := VerifyReport{SeriesDir: seriesDir, Healthy: true}
	info, err := os.Stat(seriesDir)
	if err != nil {
		return report, fmt.Errorf("stat series directory: %w", err)
	}
	if !info.IsDir() {
		return report, fmt.Errorf("%q is not a directory", seriesDir)
	}
	// Volume folders first, then flat chapter folders.
	entries, err := os.ReadDir(seriesDir)
	if err != nil {
		return report, fmt.Errorf("read series directory: %w", err)
	}
	done, err := readStateDone(filepath.Join(seriesDir, ".state.json"))
	if err != nil {
		return report, err
	}
	doneSet := make(map[float64]bool, len(done))
	for _, number := range done {
		doneSet[number] = true
	}
	report.DoneCount = len(done)
	report.Healthy = true
	var chapterDirs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if isVolumeDir(entry.Name()) {
			volDir := filepath.Join(seriesDir, entry.Name())
			volEntries, err := os.ReadDir(volDir)
			if err != nil {
				return report, fmt.Errorf("read volume directory %s: %w", entry.Name(), err)
			}
			for _, volEntry := range volEntries {
				if volEntry.IsDir() && strings.HasPrefix(volEntry.Name(), "chapter-") {
					chapterDirs = append(chapterDirs, filepath.Join(volDir, volEntry.Name()))
				}
			}
			continue
		}
		if strings.HasPrefix(entry.Name(), "chapter-") {
			chapterDirs = append(chapterDirs, filepath.Join(seriesDir, entry.Name()))
		}
	}
	sort.Strings(chapterDirs)
	for _, dir := range chapterDirs {
		chapter, err := verifyChapterDir(dir, doneSet)
		if err != nil {
			return report, err
		}
		if !doneSet[chapter.Number] {
			report.NotInState = append(report.NotInState, dir)
		}
		report.Chapters = append(report.Chapters, chapter)
		if len(chapter.BrokenPages) > 0 || len(chapter.MissingPages) > 0 {
			report.Healthy = false
			report.Problems++
		}
	}
	return report, nil
}

func verifyChapterDir(dir string, doneSet map[float64]bool) (VerifyChapter, error) {
	chapter := VerifyChapter{Dir: filepath.Base(dir)}
	// Flat layout: <series>/chapter-001[-raw-...]; mapped: <series>/vol-XX/chapter-...
	base := filepath.Base(dir)
	display := strings.TrimPrefix(base, "chapter-")
	normalized := strings.Replace(display, ".", "", 1)
	if raw := strings.Index(normalized, "-raw-"); raw >= 0 {
		normalized = normalized[:raw]
	}
	normalized = strings.TrimLeft(strings.SplitN(normalized, ".", 2)[0], "0")
	if normalized == "" {
		normalized = "0"
	}
	number, err := strconv.ParseFloat(normalized, 64)
	if err != nil {
		return chapter, fmt.Errorf("parse chapter number from %q: %w", base, err)
	}
	chapter.Chapter = display
	chapter.Number = number
	entries, err := os.ReadDir(dir)
	if err != nil {
		return chapter, fmt.Errorf("read chapter directory %s: %w", dir, err)
	}
	pages := make(map[int]string)
	var validPages int
	var strayFiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			strayFiles = append(strayFiles, entry.Name()+"/")
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			// Temporary .part files from interrupted writes are expected
			// debris, not corruption, but surfaced for visibility.
			strayFiles = append(strayFiles, name)
			continue
		}
		page, err := strconv.Atoi(strings.SplitN(name, ".", 2)[0])
		if err != nil || page < 1 {
			strayFiles = append(strayFiles, name)
			continue
		}
		filename := filepath.Join(dir, name)
		if !store.ValidResumeFile(filename) {
			chapter.BrokenPages = append(chapter.BrokenPages, name)
			continue
		}
		pages[page] = name
		validPages++
	}
	chapter.Pages = len(pages) + len(chapter.BrokenPages)
	chapter.ValidPages = validPages
	chapter.StrayFiles = strayFiles
	sort.Strings(chapter.BrokenPages)
	sort.Strings(chapter.StrayFiles)
	if len(pages) > 0 {
		maxPage := 0
		for page := range pages {
			if page > maxPage {
				maxPage = page
			}
		}
		for page := 1; page <= maxPage; page++ {
			if _, ok := pages[page]; !ok {
				chapter.MissingPages = append(chapter.MissingPages, page)
			}
		}
	}
	return chapter, nil
}

func isVolumeDir(name string) bool {
	if !strings.HasPrefix(name, "vol-") {
		return false
	}
	rest := strings.TrimPrefix(name, "vol-")
	_, err := strconv.Atoi(rest)
	return err == nil
}

func readStateDone(path string) ([]float64, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	var state store.State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode state: %w", err)
	}
	return state.Done, nil
}
