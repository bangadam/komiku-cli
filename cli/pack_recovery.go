package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/bangadam/komiku-cli/komiku"
	packer "github.com/bangadam/komiku-cli/pack"
	"github.com/bangadam/komiku-cli/store"
)

const maxLegacyStateBytes int64 = 1 << 20

type wikipediaRecovery struct {
	Plan      PackPlan
	Series    string
	Title     string
	SourceURL string
	Mappings  []komiku.Volume
	Sources   []PackChapterSource
	Ignored   []string
}

type legacyFlatChapter struct {
	Display   string
	Number    float64
	SourceDir string
}

type legacyState struct {
	Done []float64 `json:"done"`
}

func prepareWikipediaRecovery(ctx context.Context, seriesDir, titleOverride, volumeExpression string, preset packer.Preset, httpClient *http.Client, recoverComplete bool) (wikipediaRecovery, error) {
	return prepareVolumeRecovery(ctx, seriesDir, titleOverride, volumeExpression, preset, httpClient, recoverComplete, komiku.VolumeSourceMangaDex)
}

// prepareVolumeRecovery rebuilds a volume mapping for a flat download from the
// selected provider (MangaDex by default) and plans the pack.
func prepareVolumeRecovery(ctx context.Context, seriesDir, titleOverride, volumeExpression string, preset packer.Preset, httpClient *http.Client, recoverComplete bool, source komiku.VolumeSource) (wikipediaRecovery, error) {
	root, err := canonicalSeriesRoot(seriesDir)
	if err != nil {
		return wikipediaRecovery{}, err
	}
	if info, err := os.Lstat(PackManifestPath(root)); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return wikipediaRecovery{}, errors.New("existing pack manifest is not a regular non-symlink file")
		}
		return wikipediaRecovery{}, errors.New("pack manifest already exists; use the normal offline pack command")
	} else if !errors.Is(err, os.ErrNotExist) {
		return wikipediaRecovery{}, fmt.Errorf("inspect existing pack manifest: %w", err)
	}
	series := filepath.Base(root)
	if series == "" || series == "." || series == ".." || len(series) > 200 || series != strings.TrimSpace(series) || strings.ContainsAny(series, "/\\\x00") {
		return wikipediaRecovery{}, fmt.Errorf("invalid series directory name %q", series)
	}
	title, err := recoveryWikipediaTitle(series, titleOverride)
	if err != nil {
		return wikipediaRecovery{}, err
	}
	local, err := discoverLegacyFlatChapters(root)
	if err != nil {
		return wikipediaRecovery{}, err
	}
	done, err := loadLegacyDoneState(root)
	if err != nil {
		return wikipediaRecovery{}, err
	}
	for number := range done {
		if _, exists := local[number]; !exists {
			return wikipediaRecovery{}, fmt.Errorf("DONE chapter %s has no canonical flat chapter folder", formatRecoveredNumber(number))
		}
	}

	client := komiku.NewClient(redirectDisabledClient(httpClient), 0)
	maxChapter := 0
	for _, chapter := range local {
		if whole := int(chapter.Number); whole > maxChapter {
			maxChapter = whole
		}
	}
	var cache komiku.VolumeCache
	if source == komiku.VolumeSourceWikipedia {
		// --recover-wikipedia keeps its documented strict contract: exactly
		// one Wikipedia page fetch, no search resolution, no fallbacks.
		mappings, err := client.FetchWikipediaDisplayVolumesStrict(ctx, title)
		if err != nil {
			return wikipediaRecovery{}, err
		}
		cache = komiku.VolumeCache{Source: wikipediaChapterListURL(title), Volumes: mappings}
	} else {
		cache, err = client.FetchVolumeMappingFrom(ctx, title, maxChapter, source)
		if err != nil {
			return wikipediaRecovery{}, err
		}
	}
	mappings := cache.Volumes
	if len(mappings) == 0 {
		return wikipediaRecovery{}, fmt.Errorf("%w for %q; no volume mapping is available, so the chapters cannot be grouped", komiku.ErrNoWikipediaVolumeRows, title)
	}
	sourceURL := cache.Source
	selected, ignored, err := selectRecoveryMappings(mappings, local, done, volumeExpression, recoverComplete)
	if err != nil {
		return wikipediaRecovery{}, err
	}
	sources, err := buildRecoverySources(root, selected, local, done)
	if err != nil {
		return wikipediaRecovery{}, err
	}
	plan := preparePackSources(root, root, series, preset, selected, sources)
	if len(plan.Skipped) > 0 {
		return wikipediaRecovery{}, fmt.Errorf("volume %02d cannot be recovered: %s", plan.Skipped[0].Volume, plan.Skipped[0].Reason)
	}
	if plan.DisabledReason != "" || len(plan.Volumes) == 0 {
		if plan.DisabledReason == "" {
			plan.DisabledReason = "No recovered volume is complete."
		}
		return wikipediaRecovery{}, errors.New(plan.DisabledReason)
	}
	return wikipediaRecovery{
		Plan:      plan,
		Series:    series,
		Title:     title,
		SourceURL: sourceURL,
		Mappings:  selected,
		Sources:   sources,
		Ignored:   ignored,
	}, nil
}

func recoveryWikipediaTitle(series, override string) (string, error) {
	title := strings.TrimSpace(override)
	if title == "" {
		words := strings.Fields(strings.NewReplacer("-", " ", "_", " ").Replace(series))
		for index, word := range words {
			runes := []rune(word)
			if len(runes) > 0 {
				runes[0] = unicode.ToUpper(runes[0])
				words[index] = string(runes)
			}
		}
		title = strings.Join(words, " ")
	}
	if title == "" || len(title) > 200 || title != strings.TrimSpace(title) || strings.ContainsAny(title, "\x00/\\?#") || strings.Contains(title, "://") {
		return "", errors.New("Wikipedia title is empty or invalid")
	}
	for _, character := range title {
		if character < 0x20 || character == 0x7f {
			return "", errors.New("Wikipedia title contains a control character")
		}
	}
	return title, nil
}

func wikipediaChapterListURL(title string) string {
	name := strings.Join(strings.Fields(title), "_")
	return "https://en.wikipedia.org/wiki/" + url.PathEscape("List_of_"+name+"_chapters")
}

func redirectDisabledClient(client *http.Client) *http.Client {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	clone := *client
	clone.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &clone
}

func discoverLegacyFlatChapters(root string) (map[float64]legacyFlatChapter, error) {
	confined, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open series root: %w", err)
	}
	defer confined.Close()
	directory, err := confined.Open(".")
	if err != nil {
		return nil, fmt.Errorf("open series directory: %w", err)
	}
	defer directory.Close()

	chapters := make(map[float64]legacyFlatChapter)
	entriesSeen := 0
	for {
		entries, readErr := directory.ReadDir(256)
		entriesSeen += len(entries)
		if entriesSeen > maxPackManifestChapters*2 {
			return nil, fmt.Errorf("series directory entry count exceeds limit %d", maxPackManifestChapters*2)
		}
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), "chapter-") {
				continue
			}
			info, inspectErr := confined.Lstat(entry.Name())
			if inspectErr != nil {
				return nil, fmt.Errorf("inspect legacy chapter %s: %w", entry.Name(), inspectErr)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("legacy chapter %s is a symlink", entry.Name())
			}
			if !info.IsDir() {
				return nil, fmt.Errorf("legacy chapter %s is not a directory", entry.Name())
			}
			chapter, parseErr := parseLegacyFlatChapter(entry.Name())
			if parseErr != nil {
				return nil, parseErr
			}
			if previous, duplicate := chapters[chapter.Number]; duplicate {
				return nil, fmt.Errorf("legacy chapter %s duplicates %s", entry.Name(), previous.SourceDir)
			}
			chapters[chapter.Number] = chapter
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("read series directory: %w", readErr)
		}
	}
	if len(chapters) == 0 {
		return nil, errors.New("no immediate canonical flat chapter directories were found")
	}
	return chapters, nil
}

func parseLegacyFlatChapter(name string) (legacyFlatChapter, error) {
	suffix := strings.TrimPrefix(name, "chapter-")
	if suffix == name || suffix == "" {
		return legacyFlatChapter{}, fmt.Errorf("legacy chapter folder %q is malformed", name)
	}
	if strings.Contains(suffix, "-raw-") {
		return legacyFlatChapter{}, fmt.Errorf("legacy chapter folder %q has an ambiguous raw identity", name)
	}
	number, err := strconv.ParseFloat(suffix, 64)
	if err != nil || number <= 0 || math.IsNaN(number) || math.IsInf(number, 0) {
		return legacyFlatChapter{}, fmt.Errorf("legacy chapter folder %q has an invalid chapter identity", name)
	}
	display := strconv.FormatFloat(number, 'f', -1, 64)
	if store.FormatChapter(display) != suffix {
		return legacyFlatChapter{}, fmt.Errorf("legacy chapter folder %q is not canonical", name)
	}
	return legacyFlatChapter{Display: display, Number: number, SourceDir: name}, nil
}

func loadLegacyDoneState(root string) (map[float64]bool, error) {
	confined, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open series root: %w", err)
	}
	defer confined.Close()
	info, err := confined.Lstat(".state.json")
	if err != nil {
		return nil, fmt.Errorf("inspect legacy state: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("legacy .state.json must be a regular non-symlink file")
	}
	if info.Size() > maxLegacyStateBytes {
		return nil, fmt.Errorf("legacy .state.json exceeds byte limit %d", maxLegacyStateBytes)
	}
	file, err := confined.Open(".state.json")
	if err != nil {
		return nil, fmt.Errorf("open legacy state: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return nil, errors.New("legacy .state.json changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxLegacyStateBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read legacy state: %w", err)
	}
	if int64(len(data)) > maxLegacyStateBytes {
		return nil, fmt.Errorf("legacy .state.json exceeds byte limit %d", maxLegacyStateBytes)
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, fmt.Errorf("invalid legacy state JSON: %w", err)
	}
	var state legacyState
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return nil, fmt.Errorf("invalid legacy state JSON: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("invalid trailing legacy state JSON: %w", err)
	}
	if len(state.Done) == 0 {
		return nil, errors.New("legacy .state.json has no DONE chapters")
	}
	if len(state.Done) > maxPackManifestChapters {
		return nil, fmt.Errorf("legacy .state.json DONE count exceeds %d", maxPackManifestChapters)
	}
	done := make(map[float64]bool, len(state.Done))
	for _, number := range state.Done {
		if number <= 0 || number > maxPackChapterNumber || math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, fmt.Errorf("legacy .state.json has invalid DONE chapter %v", number)
		}
		if done[number] {
			return nil, fmt.Errorf("legacy .state.json repeats DONE chapter %s", formatRecoveredNumber(number))
		}
		done[number] = true
	}
	return done, nil
}

func selectRecoveryMappings(mappings []komiku.Volume, local map[float64]legacyFlatChapter, done map[float64]bool, expression string, recoverComplete bool) ([]komiku.Volume, []string, error) {
	_, volumeByChapter, err := strictVolumeChapterIndex(mappings)
	if err != nil {
		return nil, nil, err
	}
	explicit := strings.TrimSpace(expression) != ""
	if explicit {
		selected, err := SelectedVolumeMappings(mappings, expression)
		if err != nil {
			return nil, nil, err
		}
		wanted := make(map[int]bool, len(selected))
		for _, mapping := range selected {
			wanted[mapping.Volume] = true
		}
		ignored := make([]string, 0)
		for number := range local {
			if !recoveredChapterIsUsed(number, local, volumeByChapter, wanted) {
				ignored = append(ignored, formatRecoveredNumber(number))
			}
		}
		sortRecoveredNumbers(ignored)
		return selected, ignored, nil
	}
	packable := make([]int, 0)
	partial := make([]int, 0)
	for _, mapping := range mappings {
		present, complete := false, true
		for number := mapping.Start; number <= mapping.End; number++ {
			if _, exists := local[float64(number)]; exists {
				present = true
			} else {
				complete = false
			}
			if !done[float64(number)] {
				complete = false
			}
		}
		if !present {
			continue
		}
		if complete {
			packable = append(packable, mapping.Volume)
		} else {
			partial = append(partial, mapping.Volume)
		}
	}
	unmapped := make([]string, 0)
	for number, chapter := range local {
		if !done[number] {
			unmapped = append(unmapped, chapter.Display+" (not DONE)")
			continue
		}
		if number != math.Trunc(number) {
			unmapped = append(unmapped, chapter.Display)
			continue
		}
		if number > maxPackChapterNumber || volumeByChapter[int(number)] == 0 {
			unmapped = append(unmapped, chapter.Display)
		}
	}
	sort.Strings(unmapped)
	if recoverComplete {
		if len(packable) == 0 {
			return nil, nil, errors.New("no complete downloaded Wikipedia volume can be packed")
		}
		wanted := make(map[int]bool, len(packable))
		for _, volume := range packable {
			wanted[volume] = true
		}
		selected := make([]komiku.Volume, 0, len(packable))
		for _, mapping := range mappings {
			if wanted[mapping.Volume] {
				selected = append(selected, mapping)
			}
		}
		ignored := make([]string, 0)
		for number := range local {
			if !recoveredChapterIsUsed(number, local, volumeByChapter, wanted) {
				ignored = append(ignored, formatRecoveredNumber(number))
			}
		}
		sortRecoveredNumbers(ignored)
		return selected, ignored, nil
	}
	if len(partial) > 0 || len(unmapped) > 0 {
		return nil, nil, fmt.Errorf("legacy recovery is not an unambiguous set: packable volumes=%s; partial volumes=%s; unmapped/incomplete chapters=%s; specify --vol to scope recovery", formatRecoveredVolumes(packable), formatRecoveredVolumes(partial), formatRecoveredList(unmapped))
	}
	if len(packable) == 0 {
		return nil, nil, errors.New("no local chapter belongs to a complete exact Wikipedia volume row")
	}
	wanted := make(map[int]bool, len(packable))
	for _, volume := range packable {
		wanted[volume] = true
	}
	selected := make([]komiku.Volume, 0, len(packable))
	for _, mapping := range mappings {
		if wanted[mapping.Volume] {
			selected = append(selected, mapping)
		}
	}
	return selected, nil, nil
}

// fractionalPartsFor returns the local chapters that represent a whole chapter
// number. Komiku splits some chapters into fractional parts (1.1, 1.2), so a
// volume declared as 1-4 may only have 1.1/1.2 on disk instead of chapter-001.
func fractionalPartsFor(local map[float64]legacyFlatChapter, whole int) []legacyFlatChapter {
	parts := make([]legacyFlatChapter, 0, 2)
	for number, chapter := range local {
		if number > float64(whole) && number < float64(whole+1) {
			parts = append(parts, chapter)
		}
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].Number < parts[j].Number })
	return parts
}

// recoveredChapterIsUsed reports whether a local chapter is consumed by the
// selected volumes. A whole chapter is used when its volume is selected. A
// fractional part is used only when it stands in for a missing whole chapter
// (Komiku splits chapter 1 into 1.1 and 1.2); when the whole chapter exists,
// the fractional part is an extra and stays out of the archive.
func recoveredChapterIsUsed(number float64, local map[float64]legacyFlatChapter, volumeByChapter map[int]int, wanted map[int]bool) bool {
	if number <= 0 {
		return false
	}
	whole := int(math.Trunc(number))
	if whole <= 0 || whole > maxPackChapterNumber {
		return false
	}
	volume := volumeByChapter[whole]
	if volume == 0 || !wanted[volume] {
		return false
	}
	if number == math.Trunc(number) {
		return true
	}
	_, wholeExists := local[float64(whole)]
	return !wholeExists
}

func buildRecoverySources(root string, mappings []komiku.Volume, local map[float64]legacyFlatChapter, done map[float64]bool) ([]PackChapterSource, error) {
	sources := make([]PackChapterSource, 0)
	for _, mapping := range mappings {
		for number := mapping.Start; number <= mapping.End; number++ {
			chapter, exists := local[float64(number)]
			if exists {
				if !done[float64(number)] {
					return nil, fmt.Errorf("volume %02d is partial: chapter %d is not DONE in .state.json", mapping.Volume, number)
				}
				relative, pages, err := validatePackSource(root, chapter.SourceDir)
				if err != nil {
					return nil, fmt.Errorf("volume %02d chapter %d: %w", mapping.Volume, number, err)
				}
				sources = append(sources, PackChapterSource{
					Chapter:       komiku.Chapter{Display: strconv.Itoa(number), Number: float64(number)},
					Volume:        mapping.Volume,
					Dir:           relative,
					ExpectedPages: pages,
					Complete:      true,
				})
				continue
			}

			// No canonical folder: accept the fractional parts of this chapter
			// when every part is present and DONE.
			parts := fractionalPartsFor(local, number)
			if len(parts) == 0 {
				return nil, fmt.Errorf("volume %02d is partial: canonical folder chapter-%s is missing", mapping.Volume, store.FormatChapter(strconv.Itoa(number)))
			}
			for _, part := range parts {
				if !done[part.Number] {
					return nil, fmt.Errorf("volume %02d is partial: chapter %s is not DONE in .state.json", mapping.Volume, part.Display)
				}
				relative, pages, err := validatePackSource(root, part.SourceDir)
				if err != nil {
					return nil, fmt.Errorf("volume %02d chapter %s: %w", mapping.Volume, part.Display, err)
				}
				sources = append(sources, PackChapterSource{
					Chapter:       komiku.Chapter{Display: part.Display, Number: part.Number},
					Volume:        mapping.Volume,
					Dir:           relative,
					ExpectedPages: pages,
					Complete:      true,
				})
			}
		}
	}
	return sources, nil
}

func formatRecoveredVolumes(volumes []int) string {
	if len(volumes) == 0 {
		return "none"
	}
	values := make([]string, 0, len(volumes))
	for _, volume := range volumes {
		values = append(values, fmt.Sprintf("%02d", volume))
	}
	return strings.Join(values, ",")
}

func formatRecoveredList(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ",")
}

func formatRecoveredNumber(number float64) string {
	return strconv.FormatFloat(number, 'f', -1, 64)
}

func sortRecoveredNumbers(numbers []string) {
	sort.Slice(numbers, func(i, j int) bool {
		left, _ := strconv.ParseFloat(numbers[i], 64)
		right, _ := strconv.ParseFloat(numbers[j], 64)
		return left < right
	})
}
