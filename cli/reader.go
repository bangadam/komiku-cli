package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/bangadam/komiku-cli/komiku"
	"github.com/bangadam/komiku-cli/store"
)

const readCommandUsage = "usage: komiku-cli read <series-url> [--ch latest|first|N] [--cache] [--offline] [--progress] [--json]"

const (
	readerManifestVersion = 1
	maxReaderMetadata     = 16 << 20
	maxReaderImage        = 64 << 20
	maxReaderChapters     = 10000
)

// ReaderPage retains the original 1-based position, including unavailable pages.
type ReaderPage struct {
	Index  int    `json:"index"`
	URL    string `json:"url,omitempty"`
	Local  string `json:"local,omitempty"`
	Cached bool   `json:"cached"`
	Bytes  int64  `json:"bytes,omitempty"`
	Error  string `json:"error,omitempty"`
}

// ReaderChapter identifies a chapter for navigation.
type ReaderChapter struct {
	Display string  `json:"display"`
	Number  float64 `json:"number"`
	URL     string  `json:"url"`
}

// ReaderReport is what the reader screen needs to render a chapter. Done means
// downloaded by the library, never merely viewed or cached by this command.
type ReaderReport struct {
	Series    string         `json:"series"`
	SeriesURL string         `json:"series_url"`
	Chapter   ReaderChapter  `json:"chapter"`
	Chapters  []ChapterInfo  `json:"chapters"`
	Pages     []ReaderPage   `json:"pages"`
	PageCount int            `json:"page_count"`
	Prev      *ReaderChapter `json:"prev,omitempty"`
	Next      *ReaderChapter `json:"next,omitempty"`
	// Network means some pages still need the network. Offline partial reports
	// retain their actual local source (cache or library), with per-page errors.
	Source    string `json:"source"`
	CacheDir  string `json:"cache_dir,omitempty"`
	Bytes     int64  `json:"bytes"`
	ChapterAt int    `json:"chapter_index"`
	Total     int    `json:"chapter_total"`
}

func NewReadCommand(dependencies Dependencies) *cobra.Command {
	cmd := &cobra.Command{
		Use: "read <series-url>", Short: "Resolve a chapter's pages for online or offline reading",
		Args: exactOneArg(readCommandUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRead(cmd.Context(), args[0], cmd.Flags(), cmd.OutOrStdout(), dependencies)
		},
	}
	cmd.Flags().String("ch", "latest", "chapter selector: latest, first, a chapter number, or an exact raw ID")
	cmd.Flags().String("out", "", "library root")
	cmd.Flags().Bool("cache", true, "download missing pages into the local reader cache")
	cmd.Flags().Bool("refresh", false, "re-download pages already in the reader cache")
	cmd.Flags().Bool("offline", false, "use only pages already on disk; never touch the network")
	cmd.Flags().Bool("progress", false, "stream NDJSON progress events, ending with the report")
	cmd.Flags().Bool("json", false, "print the reader report as JSON")
	return cmd
}

func runRead(ctx context.Context, seriesURL string, flags *pflag.FlagSet, stdout io.Writer, dependencies Dependencies) (resultErr error) {
	parsed, err := komiku.ValidateSeriesURL(seriesURL)
	if err != nil {
		return err
	}
	series := SeriesSlug(parsed)
	if err := validateManifestString("series directory", series, 200, true); err != nil || series == "." || series == ".." {
		return fmt.Errorf("invalid series directory %q", series)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	output, _ := flags.GetString("out")
	overrides := Overrides{}
	if flags.Changed("out") {
		overrides.OutputRoot = &output
	}
	config, err := loadEffectiveConfig(dependencies, overrides)
	if err != nil {
		return err
	}
	selector, _ := flags.GetString("ch")
	offline, _ := flags.GetBool("offline")
	cache, _ := flags.GetBool("cache")
	refresh, _ := flags.GetBool("refresh")
	progress, _ := flags.GetBool("progress")
	asJSON, _ := flags.GetBool("json")
	// Offline takes precedence over refresh and cache, including filesystem writes.
	fs, err := openReaderFS(config.OutputRoot, series, cache && !offline)
	if err != nil {
		return err
	}
	defer fs.close()
	local, err := fs.chapters()
	if err != nil {
		return err
	}
	done, err := fs.done()
	if err != nil {
		return err
	}
	client := dependencies.newKomikuClient(config.ImageDelay)
	var chapters []komiku.Chapter
	var manifests map[string]readerManifest
	if offline {
		chapters, manifests, err = fs.offlineMetadata(seriesURL)
		if err == nil && len(chapters) == 0 {
			chapters = mergeReaderLocalChapters(nil, local)
		}
	} else {
		chapters, err = client.Discover(ctx, seriesURL)
		if err == nil {
			err = validateReaderChapters(chapters, seriesURL)
		}
	}
	if err != nil {
		return fmt.Errorf("read chapter navigation: %w", err)
	}
	index, err := selectReaderChapter(chapters, selector)
	if err != nil {
		return err
	}
	chapter := chapters[index]
	ambiguous := ambiguousChapterNumbers(chapters)
	numberCounts, rawCounts := map[float64]int{}, map[string]int{}
	for _, ch := range chapters {
		numberCounts[ch.Number]++
		rawCounts[ch.RawID]++
		if numberCounts[ch.Number] > 1 {
			ambiguous[ch.Number] = true
		}
	}
	report := ReaderReport{
		Series: series, SeriesURL: seriesURL, Chapter: readerChapter(chapter),
		Chapters: make([]ChapterInfo, 0, len(chapters)), Pages: []ReaderPage{},
		ChapterAt: index + 1, Total: len(chapters), Source: "network",
	}
	for _, ch := range chapters {
		report.Chapters = append(report.Chapters, ChapterInfo{
			Display: ch.Display, Number: ch.Number, URL: ch.URL, Done: done[ch.Number] && !ambiguous[ch.Number],
		})
	}
	if index > 0 {
		prev := readerChapter(chapters[index-1])
		report.Prev = &prev
	}
	if index+1 < len(chapters) {
		next := readerChapter(chapters[index+1])
		report.Next = &next
	}
	cacheRel := readerCacheRelative(chapter)
	report.CacheDir = filepath.Join(fs.base, cacheRel)
	var manifest readerManifest
	if offline {
		manifest = manifests[chapter.URL]
	} else {
		manifest, err = fs.manifest(cacheRel, seriesURL)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if manifest.Version != 0 && (manifest.Chapter.URL != chapter.URL || manifest.Chapter.RawID != chapter.RawID) {
			return errors.New("reader cache belongs to a different chapter identity")
		}
		if manifest.Version != 0 {
			manifest.Chapter = chapter
		}
	}
	library := map[int]ReaderPage{}
	// A numeric/raw folder cannot identify distinct hrefs with the same raw ID.
	if rawCounts[chapter.RawID] == 1 {
		library, err = fs.libraryPages(ctx, local, chapter, ambiguous[chapter.Number])
		if err != nil {
			return err
		}
	}
	old := manifest
	if !offline && (manifest.Version == 0 || refresh) {
		imageURLs, err := client.ChapterImages(ctx, chapter.URL)
		if err != nil {
			return fmt.Errorf("read chapter %s: %w", chapter.Display, err)
		}
		if len(imageURLs) == 0 || len(imageURLs) > store.MaxChapterPages {
			return fmt.Errorf("chapter %s returned invalid image count %d", chapter.Display, len(imageURLs))
		}
		manifest = readerManifest{Version: readerManifestVersion, SeriesURL: seriesURL, Chapter: chapter, PageCount: len(imageURLs)}
		for i, imageURL := range imageURLs {
			page := readerManifestPage{Index: i + 1, URL: imageURL}
			// Refresh does not use these bytes for this run, but retain their
			// identity/digest until replacement succeeds. A failed refresh must
			// not turn a formerly corrupt file into a trusted orphan on retry.
			if i < len(old.Pages) && old.Pages[i].URL == imageURL {
				page = old.Pages[i]
			}
			manifest.Pages = append(manifest.Pages, page)
		}
	}
	if !offline {
		manifest.Chapters = chapters
		if err := validateReaderManifest(manifest, seriesURL, cacheRel); err != nil {
			return err
		}
	}
	count := manifest.PageCount
	if count == 0 {
		report.Source = "library"
		for number := range library {
			count = max(count, number)
		}
		if count == 0 {
			return fmt.Errorf("chapter %s has no local pages or reader metadata; offline mode forbids fetching it", chapter.Display)
		}
	}
	// Legacy caches have no expected count. They can only be adopted AFTER a
	// real page list has been fetched, and never for ambiguous numeric identities.
	legacy := map[int]ReaderPage{}
	if !offline && !refresh && old.Version == 0 && !ambiguous[chapter.Number] {
		legacy, err = fs.pages(ctx, filepath.Join(".reader", "chapter-"+store.FormatChapter(chapter.Display)))
		if err != nil {
			return err
		}
	}
	libraryPaths := make(map[string]bool)
	for i := 1; i <= count; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		page := ReaderPage{Index: i}
		if manifest.Version != 0 {
			page.URL = manifest.Pages[i-1].URL
		}
		if candidate := library[i]; candidate.Cached {
			page = candidate
			libraryPaths[page.Local] = true
		} else {
			page.Error = candidate.Error
			if manifest.Version != 0 && (!refresh || offline) {
				candidate, record, readErr := fs.cachedPage(ctx, cacheRel, manifest.Pages[i-1])
				if readErr != nil {
					return readErr
				}
				if candidate.Cached || candidate.Error != "" {
					page = candidate
				}
				manifest.Pages[i-1] = record
				if !page.Cached && legacy[i].Cached {
					page = legacy[i]
				}
			}
		}
		if manifest.Version != 0 {
			page.URL = manifest.Pages[i-1].URL
		}
		if offline && !page.Cached && page.Error == "" {
			page.Error = "page is not cached; offline mode forbids fetching it"
		}
		report.Pages = append(report.Pages, page)
	}
	report.PageCount = count
	updateReaderTotals(&report, libraryPaths, offline)
	if progress {
		if err := finishRead(stdout, report, asJSON, true); err != nil {
			return err
		}
	}
	// Once a list is known, even cancellation or a persistence error ends with
	// its latest report. Writer errors are never silently discarded.
	defer func() {
		if resultErr != nil {
			markReaderPending(&report, resultErr)
		}
		updateReaderTotals(&report, libraryPaths, offline)
		resultErr = errors.Join(resultErr, finishRead(stdout, report, asJSON, progress))
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if offline || !cache {
		return nil
	}
	if err := fs.mkdir(cacheRel); err != nil {
		return fmt.Errorf("create reader cache: %w", err)
	}
	// Persist the entire expected list BEFORE the first image. A killed process
	// therefore leaves an offline-readable, explicitly partial chapter.
	if err := fs.saveManifest(cacheRel, manifest); err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	for i := range report.Pages {
		page := &report.Pages[i]
		if err := ctx.Err(); err != nil {
			markReaderPending(&report, err)
			return err
		}
		if page.Cached && (libraryPaths[page.Local] || filepath.Dir(page.Local) == report.CacheDir) {
			continue
		}
		var saved readerManifestPage
		save := func(extension string, body io.Reader) error {
			var saveErr error
			saved, saveErr = fs.saveImage(ctx, cacheRel, manifest.Pages[i], extension, body)
			return saveErr
		}
		var saveErr error
		if page.Cached {
			// Adopt only this validated legacy image, never rename/delete the old
			// cache or touch library images and .state.json.
			relative, relErr := filepath.Rel(fs.base, page.Local)
			if relErr != nil {
				return relErr
			}
			file, openErr := fs.open(relative)
			if openErr != nil {
				return openErr
			}
			saveErr = save(filepath.Ext(page.Local), file)
			file.Close()
		} else {
			saveErr = client.DownloadImage(ctx, page.URL, chapter.URL, save)
		}
		if saveErr != nil {
			if ctx.Err() != nil {
				saveErr = ctx.Err()
			}
			page.Error = saveErr.Error()
			if progress {
				if err := encoder.Encode(map[string]any{"kind": "page_failed", "page": page.Index, "pages": count, "message": page.Error}); err != nil {
					return err
				}
			}
			if ctx.Err() != nil {
				markReaderPending(&report, ctx.Err())
				return ctx.Err()
			}
			continue
		}
		manifest.Pages[i] = saved
		*page = ReaderPage{Index: saved.Index, URL: saved.URL, Local: filepath.Join(fs.base, cacheRel, saved.File), Cached: true, Bytes: saved.Bytes}
		if err := fs.saveManifest(cacheRel, manifest); err != nil {
			return err
		}
		if progress {
			if err := encoder.Encode(map[string]any{"kind": "page_cached", "page": page.Index, "pages": count, "local": page.Local, "bytes": page.Bytes}); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

func readerChapter(ch komiku.Chapter) ReaderChapter {
	return ReaderChapter{Display: ch.Display, Number: ch.Number, URL: ch.URL}
}

func markReaderPending(report *ReaderReport, err error) {
	for i := range report.Pages {
		if !report.Pages[i].Cached && report.Pages[i].Error == "" {
			report.Pages[i].Error = err.Error()
		}
	}
}

func updateReaderTotals(report *ReaderReport, library map[string]bool, offline bool) {
	report.Bytes = 0
	cached, fromLibrary := 0, 0
	for _, page := range report.Pages {
		if page.Cached {
			cached++
			report.Bytes += page.Bytes
			if library[page.Local] {
				fromLibrary++
			}
		}
	}
	switch {
	case !offline && cached < report.PageCount:
		report.Source = "network"
	case fromLibrary > 0 && fromLibrary == cached:
		report.Source = "library"
	case offline && cached == 0 && report.Source == "library":
		// Even an entirely broken legacy library is not a reader-cache hit.
	default:
		report.Source = "cache"
	}
}

func finishRead(stdout io.Writer, report ReaderReport, asJSON, progress bool) error {
	if progress {
		return json.NewEncoder(stdout).Encode(struct {
			Kind string `json:"kind"`
			ReaderReport
		}{Kind: "report", ReaderReport: report})
	}
	return emitReaderReport(stdout, report, asJSON)
}

func emitReaderReport(stdout io.Writer, report ReaderReport, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(stdout).Encode(report)
	}
	if _, err := fmt.Fprintf(stdout, "%s chapter %s  pages=%d source=%s\n", report.Series, report.Chapter.Display, report.PageCount, report.Source); err != nil {
		return err
	}
	for _, page := range report.Pages {
		location := page.Local
		if location == "" {
			location = page.URL
		}
		if page.Error != "" {
			location += " (" + page.Error + ")"
		}
		if _, err := fmt.Fprintf(stdout, "%03d  %s\n", page.Index, location); err != nil {
			return err
		}
	}
	return nil
}

func selectReaderChapter(chapters []komiku.Chapter, selector string) (int, error) {
	if len(chapters) == 0 {
		return 0, errors.New("no chapters are available in discovery or local reader metadata")
	}
	value := strings.TrimSpace(selector)
	switch strings.ToLower(value) {
	case "", "latest", "last":
		return len(chapters) - 1, nil
	case "first":
		return 0, nil
	}
	// Preserve selectOneChapter's raw-ID precedence, but don't let duplicate raw
	// IDs (distinct hrefs) silently select whichever happened to sort first.
	match := -1
	for i, ch := range chapters {
		if value == ch.URL || value == ch.RawID {
			if match >= 0 {
				return 0, fmt.Errorf("chapter %q is ambiguous; select its exact URL", selector)
			}
			match = i
		}
	}
	if match >= 0 {
		return match, nil
	}
	ch, err := selectOneChapter(chapters, value)
	if err != nil {
		return 0, err
	}
	for i := range chapters {
		if chapters[i] == ch {
			return i, nil
		}
	}
	return 0, fmt.Errorf("chapter %q was not found", selector)
}
