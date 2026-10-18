package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	_ "golang.org/x/image/webp"

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

// There is deliberately no "complete" bit: completeness is revalidated against
// every expected slot on each read. File names are leaves, never persisted paths.
type readerManifest struct {
	Version   int                  `json:"version"`
	SeriesURL string               `json:"series_url"`
	Chapter   komiku.Chapter       `json:"chapter"`
	Chapters  []komiku.Chapter     `json:"chapters"`
	PageCount int                  `json:"page_count"`
	Pages     []readerManifestPage `json:"pages"`
}

type readerManifestPage struct {
	Index  int    `json:"index"`
	URL    string `json:"url"`
	File   string `json:"file,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

func NewReadCommand(dependencies Dependencies) *cobra.Command {
	cmd := &cobra.Command{
		Use: "read <series-url>", Short: "List the pages of a chapter for online or offline reading",
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

// readerFS confines all untrusted descendants with os.Root, in addition to
// rejecting symlinks. Only the user-selected output root may resolve symlinks
// (important for macOS /var and user-configured library mounts).
type readerFS struct {
	root *os.Root
	base string
}

func openReaderFS(output, series string, create bool) (*readerFS, error) {
	if series == "" || series == "." || series == ".." || strings.ContainsAny(series, "/\\\x00") {
		return nil, errors.New("invalid reader root component")
	}
	absolute, err := filepath.Abs(output)
	if err != nil {
		return nil, err
	}
	if create {
		if err := os.MkdirAll(absolute, 0o755); err != nil {
			return nil, err
		}
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if errors.Is(err, os.ErrNotExist) && !create {
		return &readerFS{base: filepath.Join(absolute, series)}, nil
	}
	if err != nil {
		return nil, err
	}
	outputRoot, err := os.OpenRoot(resolved)
	if err != nil {
		return nil, err
	}
	defer outputRoot.Close()
	info, err := lstatRealRootPath(outputRoot, series)
	if errors.Is(err, os.ErrNotExist) && create {
		if err := outputRoot.Mkdir(series, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		info, err = lstatRealRootPath(outputRoot, series)
	}
	fs := &readerFS{base: filepath.Join(resolved, series)}
	if errors.Is(err, os.ErrNotExist) && !create {
		return fs, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("series path is not a real directory")
	}
	fs.root, err = outputRoot.OpenRoot(series)
	if err != nil {
		return nil, err
	}
	opened, err := fs.root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		fs.close()
		return nil, errors.New("series directory changed while opening")
	}
	return fs, nil
}

func (fs *readerFS) close() {
	if fs.root != nil {
		fs.root.Close()
	}
}

func (fs *readerFS) open(relative string) (*os.File, error) {
	if fs.root == nil {
		return nil, os.ErrNotExist
	}
	info, err := lstatRealRootPath(fs.root, relative)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", relative)
	}
	file, err := fs.root.Open(relative)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		file.Close()
		return nil, fmt.Errorf("%s changed while opening", relative)
	}
	return file, nil
}

func (fs *readerFS) entries(relative string) ([]os.DirEntry, error) {
	if fs.root == nil {
		return nil, nil
	}
	var info os.FileInfo
	var err error
	if relative == "." {
		info, err = fs.root.Stat(".")
	} else {
		info, err = lstatRealRootPath(fs.root, relative)
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", relative)
	}
	dir, err := fs.root.Open(relative)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	opened, err := dir.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("reader directory changed while opening")
	}
	entries, err := dir.ReadDir(maxReaderChapters*4 + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > maxReaderChapters*4 {
		return nil, errors.New("reader directory entry limit exceeded")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

func (fs *readerFS) mkdir(relative string) error {
	current := ""
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\\x00") {
			return errors.New("invalid reader directory")
		}
		current = filepath.Join(current, part)
		info, err := lstatRealRootPath(fs.root, current)
		if errors.Is(err, os.ErrNotExist) {
			if err := fs.root.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = lstatRealRootPath(fs.root, current)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", current)
		}
	}
	return nil
}

func (fs *readerFS) readJSON(relative string, value any) error {
	file, err := fs.open(relative)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxReaderMetadata+1))
	if err != nil {
		return err
	}
	if len(data) > maxReaderMetadata {
		return errors.New("reader metadata exceeds byte limit")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func (fs *readerFS) done() (map[float64]bool, error) {
	var state store.State
	err := fs.readJSON(".state.json", &state)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read download state: %w", err)
	}
	result := make(map[float64]bool, len(state.Done))
	for _, number := range state.Done {
		if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
			return nil, errors.New("invalid downloaded chapter number")
		}
		result[number] = true
	}
	return result, nil
}

var readerRawPattern = regexp.MustCompile(`^[0-9]+(?:-[0-9]+)?$`)
var readerDisplayPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)

func readerRawChapter(raw string) (komiku.Chapter, error) {
	if len(raw) > 80 || !readerRawPattern.MatchString(raw) {
		return komiku.Chapter{}, fmt.Errorf("invalid raw chapter identity %q", raw)
	}
	number, err := strconv.ParseFloat(strings.Replace(raw, "-", ".", 1), 64)
	if err != nil || math.IsInf(number, 0) || number > 1e9 {
		return komiku.Chapter{}, errors.New("chapter number exceeds reader limit")
	}
	parts := strings.SplitN(raw, "-", 2)
	display := strings.TrimLeft(parts[0], "0")
	if display == "" {
		display = "0"
	}
	if len(parts) == 2 {
		fraction := strings.TrimRight(parts[1], "0")
		if fraction == "" {
			fraction = "0"
		}
		display += "." + fraction
	}
	return komiku.Chapter{RawID: raw, Display: display, Number: number}, nil
}

type readerLocalChapter struct {
	chapter komiku.Chapter
	dir     string
	raw     bool
}

func (fs *readerFS) chapters() ([]readerLocalChapter, error) {
	var result []readerLocalChapter
	var walk func(string) error
	walk = func(parent string) error {
		entries, err := fs.entries(parent)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			name := entry.Name()
			path := filepath.Join(parent, name)
			if parent == "." && isVolumeDirName(name) {
				if err := walk(path); err != nil {
					return err
				}
				continue
			}
			if !strings.HasPrefix(name, "chapter-") {
				continue
			}
			parts := strings.SplitN(strings.TrimPrefix(name, "chapter-"), "-raw-", 2)
			if !readerDisplayPattern.MatchString(parts[0]) {
				continue
			}
			raw := strings.Replace(parts[0], ".", "-", 1)
			if len(parts) == 2 {
				raw = parts[1]
			}
			chapter, err := readerRawChapter(raw)
			if err != nil || store.FormatChapter(chapter.Display) != parts[0] {
				continue
			}
			info, err := lstatRealRootPath(fs.root, path)
			if err != nil {
				return err
			}
			if !info.IsDir() {
				return fmt.Errorf("chapter %s is not a directory", path)
			}
			if len(result) >= maxReaderChapters {
				return errors.New("local chapter count exceeds reader limit")
			}
			result = append(result, readerLocalChapter{chapter: chapter, dir: path, raw: len(parts) == 2})
		}
		return nil
	}
	if err := walk("."); err != nil {
		return nil, err
	}
	return result, nil
}

func mergeReaderLocalChapters(chapters []komiku.Chapter, local []readerLocalChapter) []komiku.Chapter {
	for _, item := range local {
		found := false
		for _, ch := range chapters {
			if ch.Number == item.chapter.Number && (!item.raw || ch.RawID == item.chapter.RawID) {
				found = true
				break
			}
		}
		if !found {
			chapters = append(chapters, item.chapter)
		}
	}
	sort.SliceStable(chapters, func(i, j int) bool {
		if chapters[i].Number == chapters[j].Number {
			return chapters[i].RawID < chapters[j].RawID
		}
		return chapters[i].Number < chapters[j].Number
	})
	return chapters
}

func (fs *readerFS) libraryPages(ctx context.Context, local []readerLocalChapter, chapter komiku.Chapter, ambiguous bool) (map[int]ReaderPage, error) {
	result := map[int]ReaderPage{}
	for _, item := range local {
		if item.chapter.Number != chapter.Number || (item.raw && item.chapter.RawID != chapter.RawID) || (!item.raw && ambiguous) {
			continue
		}
		pages, err := fs.pages(ctx, item.dir)
		if err != nil {
			return nil, err
		}
		for i, page := range pages {
			if !result[i].Cached {
				result[i] = page
			}
		}
	}
	return result, nil
}

func readerImageExtension(extension string) bool {
	switch strings.ToLower(extension) {
	case ".jpg", ".jpeg", ".png", ".webp":
		return true
	}
	return false
}

// pages preserves invalid slots and rejects duplicates rather than shifting the
// following images forward. Bounds are checked before allocating page arrays.
func (fs *readerFS) pages(ctx context.Context, dir string) (map[int]ReaderPage, error) {
	entries, err := fs.entries(dir)
	if err != nil {
		return nil, err
	}
	result := map[int]ReaderPage{}
	seen := map[int]bool{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ext := filepath.Ext(entry.Name())
		if !readerImageExtension(ext) {
			continue
		}
		number, err := strconv.Atoi(strings.TrimSuffix(entry.Name(), ext))
		if err != nil || number <= 0 {
			continue
		}
		if number > store.MaxChapterPages {
			return nil, fmt.Errorf("page index %d exceeds reader limit", number)
		}
		page := ReaderPage{Index: number}
		if seen[number] {
			page.Error = "duplicate local page index"
		} else {
			relative := filepath.Join(dir, entry.Name())
			file, err := fs.open(relative)
			if err != nil {
				return nil, err
			}
			size, _, imageErr := readerImageInfo(ctx, file)
			file.Close()
			if imageErr != nil {
				page.Error = imageErr.Error()
			} else {
				page.Local, page.Bytes, page.Cached = filepath.Join(fs.base, relative), size, true
			}
		}
		seen[number] = true
		result[number] = page
	}
	return result, nil
}

func readerCacheRelative(chapter komiku.Chapter) string {
	// Raw IDs preserve fractional aliases; URL identity also separates distinct
	// real hrefs that happen to use the same raw ID (including query variants).
	sum := sha256.Sum256([]byte(chapter.URL))
	return filepath.Join(".reader", fmt.Sprintf("chapter-%s-url-%x-raw-%s", store.FormatChapter(chapter.Display), sum[:12], chapter.RawID))
}

func readerPageStem(page readerManifestPage) string {
	sum := sha256.Sum256([]byte(page.URL))
	return fmt.Sprintf("%03d-%x", page.Index, sum[:12])
}

func validateReaderChapters(chapters []komiku.Chapter, seriesURL string) error {
	if len(chapters) == 0 || len(chapters) > maxReaderChapters {
		return errors.New("invalid or empty reader navigation list")
	}
	base, err := komiku.ValidateSeriesURL(seriesURL)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for i, ch := range chapters {
		identity, err := readerRawChapter(ch.RawID)
		if err != nil || ch.Display != identity.Display || ch.Number != identity.Number {
			return errors.New("inconsistent reader chapter identity")
		}
		u, err := url.Parse(ch.URL)
		if err != nil || len(ch.URL) > 8192 || u.User != nil || u.Scheme != base.Scheme || u.Host != base.Host || u.Opaque != "" {
			return errors.New("invalid reader chapter URL")
		}
		// Use the same scraper identity rules as real discovery, including raw
		// fractions, without treating a persisted URL as a filesystem path.
		path := strings.TrimSuffix(u.Path, "/")
		if !strings.HasPrefix(path, "/") || strings.Count(path, "/") != 1 || !strings.HasSuffix(path, "-chapter-"+ch.RawID) || strings.ContainsAny(u.Path, "\\\x00") {
			return errors.New("chapter URL does not match its raw identity")
		}
		if ch.Href != "" {
			href, err := url.Parse(ch.Href)
			if err != nil || base.ResolveReference(href).String() != ch.URL {
				return errors.New("inconsistent reader chapter href")
			}
		}
		if seen[ch.URL] || (i > 0 && chapters[i-1].Number > ch.Number) {
			return errors.New("duplicate or unordered reader navigation")
		}
		seen[ch.URL] = true
	}
	return nil
}

func validateReaderManifest(m readerManifest, seriesURL, dir string) error {
	if m.Version != readerManifestVersion || m.SeriesURL != seriesURL || m.PageCount <= 0 || m.PageCount > store.MaxChapterPages || len(m.Pages) != m.PageCount {
		return errors.New("invalid reader manifest header or expected page count")
	}
	if err := validateReaderChapters(m.Chapters, seriesURL); err != nil {
		return err
	}
	found := false
	for _, ch := range m.Chapters {
		if ch == m.Chapter {
			found = true
		}
	}
	if !found || readerCacheRelative(m.Chapter) != dir {
		return errors.New("reader manifest chapter identity does not match its directory/navigation")
	}
	for i, page := range m.Pages {
		u, err := url.Parse(page.URL)
		if page.Index != i+1 || err != nil || len(page.URL) > 8192 || u.Host == "" || u.User != nil || u.Opaque != "" || (u.Scheme != "http" && u.Scheme != "https") {
			return errors.New("invalid reader manifest page index or URL")
		}
		if page.File == "" {
			if page.Bytes != 0 || page.SHA256 != "" {
				return errors.New("reader manifest has image metadata without a file")
			}
			continue
		}
		ext := filepath.Ext(page.File)
		digest, err := hex.DecodeString(page.SHA256)
		if !readerImageExtension(ext) || page.File != readerPageStem(page)+ext || page.Bytes <= 12 || page.Bytes > maxReaderImage || err != nil || len(digest) != sha256.Size {
			return errors.New("invalid reader manifest image filename, size, or checksum")
		}
	}
	return nil
}

func (fs *readerFS) manifest(dir, seriesURL string) (readerManifest, error) {
	var manifest readerManifest
	if err := fs.readJSON(filepath.Join(dir, "manifest.json"), &manifest); err != nil {
		return readerManifest{}, err
	}
	if err := validateReaderManifest(manifest, seriesURL, dir); err != nil {
		return readerManifest{}, fmt.Errorf("invalid reader metadata in %s: %w", dir, err)
	}
	return manifest, nil
}

func (fs *readerFS) offlineMetadata(seriesURL string) ([]komiku.Chapter, map[string]readerManifest, error) {
	entries, err := fs.entries(".reader")
	if err != nil {
		return nil, nil, err
	}
	manifests := map[string]readerManifest{}
	var chapters []komiku.Chapter
	var latest time.Time
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "chapter-") {
			continue
		}
		dir := filepath.Join(".reader", entry.Name())
		m, err := fs.manifest(dir, seriesURL)
		if errors.Is(err, os.ErrNotExist) {
			continue // Pre-manifest caches cannot establish an offline page count.
		}
		if err != nil {
			return nil, nil, err
		}
		info, err := lstatRealRootPath(fs.root, filepath.Join(dir, "manifest.json"))
		if err != nil {
			return nil, nil, err
		}
		if chapters == nil || info.ModTime().After(latest) {
			latest, chapters = info.ModTime(), m.Chapters
		}
		manifests[m.Chapter.URL] = m
	}
	return chapters, manifests, nil
}

func (fs *readerFS) cachedPage(ctx context.Context, dir string, record readerManifestPage) (ReaderPage, readerManifestPage, error) {
	page := ReaderPage{Index: record.Index, URL: record.URL}
	files := []string{record.File}
	if record.File == "" {
		files = nil
		for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp"} {
			files = append(files, readerPageStem(record)+ext)
		}
	}
	original := record
	for _, name := range files {
		file, err := fs.open(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return page, record, err
		}
		size, digest, imageErr := readerImageInfo(ctx, file)
		file.Close()
		if imageErr == nil && original.File != "" && (original.Bytes != size || original.SHA256 != digest) {
			imageErr = errors.New("cached image size or checksum mismatch")
		}
		if imageErr != nil {
			page.Error = imageErr.Error()
			continue
		}
		if page.Cached {
			return ReaderPage{Index: record.Index, URL: record.URL, Error: "duplicate cached page index"}, readerManifestPage{Index: record.Index, URL: record.URL}, nil
		}
		page = ReaderPage{Index: record.Index, URL: record.URL, Local: filepath.Join(fs.base, dir, name), Cached: true, Bytes: size}
		record.File, record.Bytes, record.SHA256 = name, size, digest
	}
	// Keep an existing checksum even for a missing/corrupt file. Clearing it
	// before a failed repair would let the next run adopt corrupt bytes as an
	// unrecorded (interrupted) image.
	return page, record, nil
}

type readerContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r readerContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// Validate supported image types, bounded dimensions, decoding and checksum.
// Unlike ValidResumeFile, legitimate small images are reusable; magic alone
// cannot distinguish an interrupted download from a usable image.
func readerImageInfo(ctx context.Context, file *os.File) (int64, string, error) {
	info, err := file.Stat()
	if err != nil {
		return 0, "", err
	}
	if info.Size() <= 12 || info.Size() > maxReaderImage {
		return 0, "", errors.New("invalid image size")
	}
	var header [12]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		return 0, "", err
	}
	if _, valid := komiku.DetectImage(header[:]); !valid {
		return 0, "", errors.New("invalid image payload")
	}
	hash := sha256.New()
	hash.Write(header[:])
	n, err := io.Copy(hash, io.LimitReader(readerContextReader{ctx, file}, maxReaderImage+1))
	if err != nil {
		return 0, "", err
	}
	if n+12 != info.Size() {
		return 0, "", errors.New("image size changed while reading")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, "", err
	}
	config, _, err := image.DecodeConfig(readerContextReader{ctx, file})
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > 32_000_000 || config.Height > 32_000_000/config.Width {
		return 0, "", errors.New("invalid image header or dimensions")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, "", err
	}
	if _, _, err := image.Decode(readerContextReader{ctx, file}); err != nil {
		return 0, "", fmt.Errorf("corrupt or truncated image: %w", err)
	}
	return info.Size(), hex.EncodeToString(hash.Sum(nil)), ctx.Err()
}

// atomicFile never follows an existing target and never removes other data.
// os.Root confines both the temporary file and rename even during path races.
func (fs *readerFS) atomicFile(relative string, write func(*os.File) error) error {
	parent := filepath.Dir(relative)
	info, err := lstatRealRootPath(fs.root, parent)
	if err != nil {
		return err
	}
	// Anchor to the cache directory as well as the series root, so replacing
	// a cache parent with an in-root symlink cannot redirect a write to library.
	confined, err := fs.root.OpenRoot(parent)
	if err != nil {
		return err
	}
	defer confined.Close()
	opened, err := confined.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("reader cache changed while opening")
	}
	leaf := filepath.Base(relative)
	if info, err := lstatRealRootPath(confined, leaf); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("reader target is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := fmt.Sprintf(".reader-%x.part", nonce)
	file, err := confined.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer confined.Remove(tmp)
	defer file.Close()
	if err := write(file); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := confined.Rename(tmp, leaf); err != nil {
		return err
	}
	dir, err := confined.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (fs *readerFS) saveManifest(dir string, manifest readerManifest) error {
	if err := validateReaderManifest(manifest, manifest.SeriesURL, dir); err != nil {
		return err
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if len(data) > maxReaderMetadata {
		return errors.New("reader metadata exceeds byte limit")
	}
	return fs.atomicFile(filepath.Join(dir, "manifest.json"), func(file *os.File) error {
		_, err := file.Write(append(data, '\n'))
		return err
	})
}

func (fs *readerFS) saveImage(ctx context.Context, dir string, page readerManifestPage, extension string, body io.Reader) (readerManifestPage, error) {
	if !readerImageExtension(extension) {
		return page, errors.New("unsupported reader image extension")
	}
	page.File = readerPageStem(page) + strings.ToLower(extension)
	err := fs.atomicFile(filepath.Join(dir, page.File), func(file *os.File) error {
		n, err := io.Copy(file, io.LimitReader(readerContextReader{ctx, body}, maxReaderImage+1))
		if err != nil {
			return err
		}
		if n > maxReaderImage {
			return errors.New("reader image exceeds byte limit")
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
		page.Bytes, page.SHA256, err = readerImageInfo(ctx, file)
		return err
	})
	return page, err
}

// Small compatibility helpers for local callers/tests. The command uses the
// confined readerFS directly so safety errors are not discarded.
func localChapters(seriesDir string) ([]komiku.Chapter, error) {
	fs, err := openReaderFS(filepath.Dir(seriesDir), filepath.Base(seriesDir), false)
	if err != nil {
		return nil, err
	}
	defer fs.close()
	local, err := fs.chapters()
	if err != nil {
		return nil, err
	}
	return mergeReaderLocalChapters(nil, local), nil
}

func findLocalChapterDir(seriesDir, display string) string {
	if !readerDisplayPattern.MatchString(display) {
		return ""
	}
	fs, err := openReaderFS(filepath.Dir(seriesDir), filepath.Base(seriesDir), false)
	if err != nil {
		return ""
	}
	defer fs.close()
	local, err := fs.chapters()
	if err != nil {
		return ""
	}
	for _, item := range local {
		if item.chapter.Display == display && !item.raw {
			return filepath.Join(seriesDir, item.dir)
		}
	}
	return ""
}

func localPages(dir string) []ReaderPage {
	fs, err := openReaderFS(filepath.Dir(dir), filepath.Base(dir), false)
	if err != nil {
		return nil
	}
	defer fs.close()
	pages, err := fs.pages(context.Background(), ".")
	if err != nil {
		return nil
	}
	result := make([]ReaderPage, 0, len(pages))
	for _, page := range pages {
		if page.Cached {
			result = append(result, page)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Index < result[j].Index })
	return result
}
