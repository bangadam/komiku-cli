package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bangadam/komiku-cli/komiku"
	"github.com/bangadam/komiku-cli/store"
)

const libraryCommandUsage = "usage: komiku-cli library [--out DIR] [--json]"

// LibrarySeries is one series row in the library dashboard.
type LibrarySeries struct {
	Slug           string   `json:"slug"`
	Dir            string   `json:"dir"`
	Chapters       int      `json:"chapters"` // chapter dirs on disk
	Done           int      `json:"done"`     // chapters marked done in state
	CBZ            int      `json:"cbz"`      // packed CBZ archives
	Bytes          int64    `json:"bytes"`    // total disk usage
	Healthy        bool     `json:"healthy"`  // no broken pages / gaps
	Problems       int      `json:"problems"` // chapters with broken pages or gaps
	ProblemsDetail []string `json:"problems_detail,omitempty"`
	Cover          string   `json:"cover,omitempty"` // local cover file, when saved
	// SeriesURL is the recorded source URL when one is known (pack manifest or
	// subscription). The GUI must not guess it from the directory slug.
	SeriesURL string `json:"series_url,omitempty"`
}

// LibraryReport is the full offline library dashboard.
type LibraryReport struct {
	Root          string          `json:"root"`
	Series        []LibrarySeries `json:"series"`
	TotalChapters int             `json:"total_chapters"`
	TotalDone     int             `json:"total_done"`
	TotalCBZ      int             `json:"total_cbz"`
	TotalBytes    int64           `json:"total_bytes"`
	TotalProblems int             `json:"total_problems"`
}

func NewLibraryCommand(dependencies Dependencies) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "library",
		Short: "Show an offline dashboard of every series in the library",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out, _ := cmd.Flags().GetString("out")
			asJSON, _ := cmd.Flags().GetBool("json")
			return runLibrary(out, asJSON, cmd.OutOrStdout(), dependencies)
		},
	}
	cmd.Flags().String("out", "", "library root to scan")
	cmd.Flags().Bool("json", false, "print the dashboard as JSON")
	return cmd
}

func runLibrary(output string, asJSON bool, stdout io.Writer, dependencies Dependencies) error {
	overrides := Overrides{}
	if output != "" {
		overrides.OutputRoot = &output
	}
	config, err := loadEffectiveConfig(dependencies, overrides)
	if err != nil {
		return err
	}
	report, err := scanLibrary(config.OutputRoot)
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(stdout).Encode(report)
	}
	fmt.Fprintf(stdout, "library: %s  series=%d  chapters=%d  done=%d  cbz=%d  size=%s  problems=%d\n",
		report.Root, len(report.Series), report.TotalChapters, report.TotalDone, report.TotalCBZ, humanBytes(report.TotalBytes), report.TotalProblems)
	if len(report.Series) == 0 {
		fmt.Fprintln(stdout, "no series downloaded yet")
		return nil
	}
	for _, s := range report.Series {
		status := "OK"
		if s.Problems > 0 {
			status = fmt.Sprintf("BROKEN(%d)", s.Problems)
		}
		fmt.Fprintf(stdout, "%-8s chapters=%-4d done=%-4d cbz=%-3d size=%-8s %s\n", s.Slug, s.Chapters, s.Done, s.CBZ, humanBytes(s.Bytes), status)
		for _, detail := range s.ProblemsDetail {
			fmt.Fprintf(stdout, "  %s\n", detail)
		}
	}
	return nil
}

// scanLibrary walks the output root and builds the dashboard.
// recordedSeriesURL returns the source URL the CLI actually recorded for this
// series. It deliberately never derives one from the directory name: a slug is
// not a URL, and guessing produced 404s for renamed or fixture series.
func recordedSeriesURL(dir string) string {
	if manifest, err := LoadPackManifest(dir); err == nil {
		return manifest.SeriesURL
	}
	return ""
}

func scanLibrary(root string) (LibraryReport, error) {
	report := LibraryReport{Root: root}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return report, nil
		}
		return report, fmt.Errorf("read library root: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		slug := entry.Name()
		dir := filepath.Join(root, slug)
		// Skip non-series dirs (no state, no chapters, no cbz).
		if !looksLikeSeries(dir) {
			continue
		}
		s := LibrarySeries{Slug: slug, Dir: dir, Healthy: true}
		if cover, ok := existingCover(dir); ok {
			s.Cover = cover
		}
		s.SeriesURL = recordedSeriesURL(dir)

		// Count chapters from directory layout (flat + mapped).
		chapterDirs := collectChapterDirs(dir)
		s.Chapters = len(chapterDirs)

		// Done chapters from state.
		if done, err := store.ReadDone(root, slug); err == nil {
			s.Done = len(done)
		}

		// CBZ count.
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".cbz") {
					s.CBZ++
				}
			}
		}

		// Disk usage + integrity per chapter dir.
		for _, chapterDir := range chapterDirs {
			bytes, err := dirSize(chapterDir)
			if err == nil {
				s.Bytes += bytes
			}
			if broken, missing := chapterProblems(chapterDir); broken > 0 || missing > 0 {
				s.Problems++
				s.Healthy = false
				s.ProblemsDetail = append(s.ProblemsDetail, fmt.Sprintf("%s: broken=%d missing=%d", filepath.Base(chapterDir), broken, missing))
			}
		}
		// CBZ archives count toward disk usage too.
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".cbz") {
					if info, err := e.Info(); err == nil {
						s.Bytes += info.Size()
					}
				}
			}
		}

		report.Series = append(report.Series, s)
		report.TotalChapters += s.Chapters
		report.TotalDone += s.Done
		report.TotalCBZ += s.CBZ
		report.TotalBytes += s.Bytes
		report.TotalProblems += s.Problems
	}
	sort.Slice(report.Series, func(i, j int) bool { return report.Series[i].Slug < report.Series[j].Slug })
	return report, nil
}

// looksLikeSeries reports whether a directory is a series download: it has a
// state file, a pack manifest, chapter dirs, or CBZ archives.
func looksLikeSeries(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, ".state.json")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, ".pack.json")); err == nil {
		return true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() && (strings.HasPrefix(e.Name(), "chapter-") || strings.HasPrefix(e.Name(), "vol-")) {
			return true
		}
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".cbz") {
			return true
		}
	}
	return false
}

type localSeriesTarget struct {
	Slug string
	URL  string
}

// localSeriesTargets resolves URLs for the series already present in the library.
func localSeriesTargets(root string, dependencies Dependencies) ([]localSeriesTarget, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read library root: %w", err)
	}
	var targets []localSeriesTarget
	for _, entry := range entries {
		if !entry.IsDir() || !looksLikeSeries(filepath.Join(root, entry.Name())) {
			continue
		}
		seriesURL, err := localSeriesURL(root, entry.Name(), dependencies.BaseURL)
		if err != nil {
			continue
		}
		targets = append(targets, localSeriesTarget{Slug: entry.Name(), URL: seriesURL})
	}
	return targets, nil
}

// localSeriesURL prefers the recorded URL and falls back to the directory slug
// for older downloads without a pack manifest.
func localSeriesURL(root, slug, baseURL string) (string, error) {
	if manifest, err := LoadPackManifest(filepath.Join(root, slug)); err == nil && manifest.SeriesURL != "" {
		return manifest.SeriesURL, nil
	}
	if baseURL == "" {
		baseURL = komiku.DefaultBaseURL
	}
	seriesURL := strings.TrimSuffix(baseURL, "/") + "/manga/" + url.PathEscape(slug) + "/"
	if _, err := komiku.ValidateSeriesURL(seriesURL); err != nil {
		return "", err
	}
	return seriesURL, nil
}

// seriesHasDownloads excludes metadata-only and empty placeholder directories
// from updates. Partial chapter pages still count so failed downloads can resume.
func seriesHasDownloads(dir string) bool {
	if done, err := store.ReadDone(filepath.Dir(dir), filepath.Base(dir)); err == nil && len(done) > 0 {
		return true
	}
	if manifest, err := LoadPackManifest(dir); err == nil && len(manifest.Chapters) > 0 {
		return true
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if strings.EqualFold(filepath.Ext(entry.Name()), ".cbz") {
			if info, err := entry.Info(); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
				return true
			}
		}
	}
	for _, chapterDir := range collectChapterDirs(dir) {
		pages, _ := os.ReadDir(chapterDir)
		for _, page := range pages {
			ext := filepath.Ext(page.Name())
			switch strings.ToLower(ext) {
			case ".jpg", ".jpeg", ".png", ".webp":
				number, err := strconv.Atoi(strings.TrimSuffix(page.Name(), ext))
				if err != nil || number < 1 {
					continue
				}
				if info, err := page.Info(); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
					return true
				}
			}
		}
	}
	return false
}

// collectChapterDirs returns every chapter directory, mapped or flat.
func collectChapterDirs(dir string) []string {
	var out []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), "chapter-") {
			out = append(out, filepath.Join(dir, e.Name()))
			continue
		}
		if isVolumeDirName(e.Name()) {
			volDir := filepath.Join(dir, e.Name())
			volEntries, err := os.ReadDir(volDir)
			if err != nil {
				continue
			}
			for _, ve := range volEntries {
				if ve.IsDir() && strings.HasPrefix(ve.Name(), "chapter-") {
					out = append(out, filepath.Join(volDir, ve.Name()))
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func isVolumeDirName(name string) bool {
	if !strings.HasPrefix(name, "vol-") {
		return false
	}
	rest := strings.TrimPrefix(name, "vol-")
	if rest == "" {
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// dirSize sums the bytes of all files under dir (non-recursive chapter dirs
// hold pages directly).
func dirSize(dir string) (int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += info.Size()
	}
	return total, nil
}

// chapterProblems counts broken (invalid magic bytes) and missing page
// numbers within one chapter directory.
func chapterProblems(dir string) (broken, missing int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	present := make(map[int]bool)
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		var page int
		if _, err := fmt.Sscanf(e.Name(), "%d", &page); err != nil || page < 1 {
			continue
		}
		if !store.ValidResumeFile(filepath.Join(dir, e.Name())) {
			broken++
			continue
		}
		present[page] = true
	}
	maxPage := 0
	for page := range present {
		if page > maxPage {
			maxPage = page
		}
	}
	for page := 1; page <= maxPage; page++ {
		if !present[page] {
			missing++
		}
	}
	return broken, missing
}

// humanBytes renders a byte count in a compact human form.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", float64(n)/float64(div), "KMGTPE"[exp])
}
