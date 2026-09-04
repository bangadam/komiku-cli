package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bangadam/komiku-cli/store"
)

const libraryCommandUsage = "usage: komiku-cli library [--out DIR] [--json]"

// LibrarySeries is one series row in the library dashboard.
type LibrarySeries struct {
	Slug           string   `json:"slug"`
	Dir            string   `json:"dir"`
	Chapters       int      `json:"chapters"`        // chapter dirs on disk
	Done           int      `json:"done"`            // chapters marked done in state
	CBZ            int      `json:"cbz"`             // packed CBZ archives
	Bytes          int64    `json:"bytes"`           // total disk usage
	Subscribed     bool     `json:"subscribed"`      // tracked for updates
	Healthy        bool     `json:"healthy"`         // no broken pages / gaps
	Problems       int      `json:"problems"`        // chapters with broken pages or gaps
	ProblemsDetail []string `json:"problems_detail,omitempty"`
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
	report, err := scanLibrary(config.OutputRoot, dependencies)
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
		sub := ""
		if s.Subscribed {
			sub = " [subscribed]"
		}
		fmt.Fprintf(stdout, "%-8s chapters=%-4d done=%-4d cbz=%-3d size=%-8s %s%s\n", s.Slug, s.Chapters, s.Done, s.CBZ, humanBytes(s.Bytes), status, sub)
		for _, detail := range s.ProblemsDetail {
			fmt.Fprintf(stdout, "  %s\n", detail)
		}
	}
	return nil
}

// scanLibrary walks the output root and builds the dashboard. It also
// cross-references the subscription list to flag tracked series.
func scanLibrary(root string, dependencies Dependencies) (LibraryReport, error) {
	report := LibraryReport{Root: root}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return report, nil
		}
		return report, fmt.Errorf("read library root: %w", err)
	}

	subsPath := dependencies.subsPath()
	subs, subsErr := LoadSubscriptions(subsPath)
	subscribed := make(map[string]bool)
	if subsErr == nil {
		for _, sub := range subs.Subscriptions {
			subscribed[sub.Slug] = true
		}
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
		s := LibrarySeries{Slug: slug, Dir: dir, Healthy: true, Subscribed: subscribed[slug]}

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
