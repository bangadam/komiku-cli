package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bangadam/komiku-cli/komiku"
	packer "github.com/bangadam/komiku-cli/pack"
	"github.com/bangadam/komiku-cli/store"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type ResolvedVolumeSelection struct {
	Mappings []komiku.Volume
	Jobs     []Job
}

// ResolveCompleteVolumeSelection accepts only an exact union of complete, non-overlapping
// Wikipedia volume rows. It never assigns extras, decimal chapters, or partial rows.
func ResolveCompleteVolumeSelection(chapters []komiku.Chapter, selected map[string]bool, mappings []komiku.Volume) (ResolvedVolumeSelection, error) {
	rows, volumeByChapter, err := strictVolumeChapterIndex(mappings)
	if err != nil {
		return ResolvedVolumeSelection{}, err
	}

	discovered := make(map[int][]komiku.Chapter, len(chapters))
	for _, chapter := range chapters {
		if chapter.Number <= 0 || chapter.Number != math.Trunc(chapter.Number) {
			continue
		}
		number := int(chapter.Number)
		discovered[number] = append(discovered[number], chapter)
	}

	selectedCount := 0
	chosen := make(map[int]bool)
	assignment := make(map[string]int)
	for _, chapter := range chapters {
		if !selected[chapter.URL] {
			continue
		}
		if _, duplicate := assignment[chapter.URL]; duplicate {
			return ResolvedVolumeSelection{}, fmt.Errorf("selected chapter URL %q is duplicated", chapter.URL)
		}
		selectedCount++
		if chapter.Number <= 0 || chapter.Number != math.Trunc(chapter.Number) {
			return ResolvedVolumeSelection{}, fmt.Errorf("selected chapter %s is an extra or non-integer chapter", chapter.Display)
		}
		number := int(chapter.Number)
		volume := volumeByChapter[number]
		if volume == 0 {
			return ResolvedVolumeSelection{}, fmt.Errorf("selected chapter %s is outside the Wikipedia volume rows", chapter.Display)
		}
		chosen[volume] = true
		assignment[chapter.URL] = volume
	}
	if selectedCount == 0 {
		return ResolvedVolumeSelection{}, errors.New("no chapters are selected")
	}
	selectedKeys := 0
	for _, isSelected := range selected {
		if isSelected {
			selectedKeys++
		}
	}
	if selectedKeys != selectedCount {
		return ResolvedVolumeSelection{}, errors.New("selection contains an undiscovered or duplicate chapter identity")
	}

	selectedMappings := make([]komiku.Volume, 0, len(chosen))
	for _, row := range rows {
		if !chosen[row.Volume] {
			continue
		}
		for number := row.Start; number <= row.End; number++ {
			matches := discovered[number]
			if len(matches) == 0 {
				return ResolvedVolumeSelection{}, fmt.Errorf("volume %02d requires chapter %d, but it was not discovered", row.Volume, number)
			}
			if len(matches) > 1 {
				return ResolvedVolumeSelection{}, fmt.Errorf("chapter %d has ambiguous discovered identities", number)
			}
			if !selected[matches[0].URL] {
				return ResolvedVolumeSelection{}, fmt.Errorf("volume %02d requires selected chapter %d", row.Volume, number)
			}
		}
		selectedMappings = append(selectedMappings, row)
	}

	jobs := make([]Job, 0, selectedCount)
	for _, chapter := range chapters {
		if !selected[chapter.URL] {
			continue
		}
		volume := assignment[chapter.URL]
		if volume == 0 {
			return ResolvedVolumeSelection{}, fmt.Errorf("selected chapter %s has no unique Wikipedia volume", chapter.Display)
		}
		jobs = append(jobs, Job{Chapter: chapter, Volume: volume})
	}
	return ResolvedVolumeSelection{Mappings: selectedMappings, Jobs: jobs}, nil
}

func strictVolumeChapterIndex(mappings []komiku.Volume) ([]komiku.Volume, map[int]int, error) {
	if len(mappings) == 0 {
		return nil, nil, errors.New("Wikipedia volume mapping is unavailable")
	}
	if len(mappings) > maxPackManifestMappings {
		return nil, nil, fmt.Errorf("Wikipedia volume mapping exceeds limit %d", maxPackManifestMappings)
	}
	rows := append([]komiku.Volume(nil), mappings...)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Start == rows[j].Start {
			return rows[i].Volume < rows[j].Volume
		}
		return rows[i].Start < rows[j].Start
	})
	seenVolumes := make(map[int]bool, len(rows))
	volumeByChapter := make(map[int]int)
	totalCoverage := 0
	for index, row := range rows {
		if row.Volume <= 0 || row.Volume > maxSelectedVolume || row.Start <= 0 || row.End < row.Start || row.End > maxPackChapterNumber || row.End-row.Start+1 > store.MaxChapterPages {
			return nil, nil, fmt.Errorf("Wikipedia volume %02d has an invalid bounded chapter range", row.Volume)
		}
		if seenVolumes[row.Volume] {
			return nil, nil, fmt.Errorf("Wikipedia volume %02d is duplicated", row.Volume)
		}
		seenVolumes[row.Volume] = true
		if index > 0 && row.Start <= rows[index-1].End {
			return nil, nil, fmt.Errorf("Wikipedia volumes %02d and %02d overlap", rows[index-1].Volume, row.Volume)
		}
		width := row.End - row.Start + 1
		if width > maxPackManifestChapters-totalCoverage {
			return nil, nil, fmt.Errorf("Wikipedia volume mapping covers more than %d chapters", maxPackManifestChapters)
		}
		totalCoverage += width
		for number := row.Start; number <= row.End; number++ {
			volumeByChapter[number] = row.Volume
		}
	}
	return rows, volumeByChapter, nil
}

var errPackManifestNotFound = errors.New("pack manifest not found")

const PackManifestVersion = 1

type PackManifest struct {
	Version   int                   `json:"version"`
	Series    string                `json:"series"`
	SeriesURL string                `json:"series_url,omitempty"`
	Mappings  []PackManifestMapping `json:"mappings"`
	Chapters  []PackManifestChapter `json:"chapters"`
}

type PackManifestMapping struct {
	Volume     int    `json:"volume"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
	Provenance string `json:"provenance"`
}

type PackManifestChapter struct {
	URL                string  `json:"url,omitempty"`
	RawID              string  `json:"raw_id,omitempty"`
	IdentityProvenance string  `json:"identity_provenance"`
	Display            string  `json:"display"`
	Number             float64 `json:"number"`
	Volume             int     `json:"volume"`
	SourceDir          string  `json:"source_dir"`
	ExpectedPages      int     `json:"expected_pages"`
	Completed          bool    `json:"completed"`
}

func PackManifestPath(seriesDir string) string {
	return filepath.Join(seriesDir, ".pack.json")
}

func RecordPackManifest(seriesDir, series, seriesURL, provenance string, mappings []komiku.Volume, jobs []Job, results []Result) error {
	if provenance == "" {
		return errors.New("pack manifest mapping provenance is empty")
	}
	root, err := canonicalSeriesRoot(seriesDir)
	if err != nil {
		return err
	}
	release, err := acquireManifestLock(root)
	if err != nil {
		return err
	}
	defer release()
	manifest, err := loadPackManifestIfPresent(root)
	if err != nil {
		return err
	}
	if manifest.Version == 0 {
		manifest = PackManifest{Version: PackManifestVersion, Series: series, SeriesURL: seriesURL}
	}
	if manifest.Version != PackManifestVersion {
		return fmt.Errorf("unsupported pack manifest version %d", manifest.Version)
	}
	if manifest.Series != series {
		return fmt.Errorf("pack manifest series conflict: %q != %q", manifest.Series, series)
	}
	if manifest.SeriesURL != "" && seriesURL != "" && manifest.SeriesURL != seriesURL {
		return fmt.Errorf("pack manifest series URL conflict: %q != %q", manifest.SeriesURL, seriesURL)
	}
	if manifest.SeriesURL == "" {
		manifest.SeriesURL = seriesURL
	}

	mappingByVolume := make(map[int]PackManifestMapping, len(manifest.Mappings)+len(mappings))
	for _, mapping := range manifest.Mappings {
		mappingByVolume[mapping.Volume] = mapping
	}
	for _, mapping := range mappings {
		incoming := PackManifestMapping{Volume: mapping.Volume, Start: mapping.Start, End: mapping.End, Provenance: provenance}
		if existing, ok := mappingByVolume[mapping.Volume]; ok {
			if existing.Volume != incoming.Volume || existing.Start != incoming.Start || existing.End != incoming.End {
				return fmt.Errorf("pack manifest volume %02d mapping conflicts with existing data", mapping.Volume)
			}
			continue
		}
		mappingByVolume[mapping.Volume] = incoming
	}

	resultByKey := make(map[string]Result, len(results))
	for _, result := range results {
		key := manifestChapterKey(result.Chapter.URL, result.Chapter.RawID, result.Chapter.Number)
		if _, duplicate := resultByKey[key]; duplicate {
			return fmt.Errorf("completed chapter result %s is duplicated", result.Chapter.Display)
		}
		resultByKey[key] = result
	}
	chapterByKey := make(map[string]PackManifestChapter, len(manifest.Chapters)+len(jobs))
	for _, chapter := range manifest.Chapters {
		chapterByKey[manifestChapterKey(chapter.URL, chapter.RawID, chapter.Number)] = chapter
	}
	for _, job := range jobs {
		requiredVolume := requiredChapterVolume(job.Chapter.Number, mappings)
		if requiredVolume == 0 || job.Volume != requiredVolume {
			continue
		}
		key := manifestChapterKey(job.Chapter.URL, job.Chapter.RawID, job.Chapter.Number)
		result, ok := resultByKey[key]
		if !ok || result.Status != Done {
			continue
		}
		if result.Total <= 0 || result.SourceDir == "" {
			return fmt.Errorf("completed chapter %s lacks page count or source directory", job.Chapter.Display)
		}
		relative, pages, err := validatePackSource(root, result.SourceDir)
		if err != nil {
			return fmt.Errorf("completed chapter %s source: %w", job.Chapter.Display, err)
		}
		if pages != result.Total {
			return fmt.Errorf("completed chapter %s has %d of %d expected pages", job.Chapter.Display, pages, result.Total)
		}
		incoming := PackManifestChapter{URL: job.Chapter.URL, RawID: job.Chapter.RawID, IdentityProvenance: "komiku-download", Display: job.Chapter.Display, Number: job.Chapter.Number, Volume: job.Volume, SourceDir: filepath.ToSlash(relative), ExpectedPages: result.Total, Completed: true}
		key = manifestChapterKey(incoming.URL, incoming.RawID, incoming.Number)
		if existing, exists := chapterByKey[key]; exists && existing != incoming {
			return fmt.Errorf("pack manifest chapter %s conflicts with existing identity, mapping, path, or page count", job.Chapter.Display)
		}
		chapterByKey[key] = incoming
	}

	manifest.Mappings = manifest.Mappings[:0]
	for _, mapping := range mappingByVolume {
		manifest.Mappings = append(manifest.Mappings, mapping)
	}
	sort.Slice(manifest.Mappings, func(i, j int) bool { return manifest.Mappings[i].Volume < manifest.Mappings[j].Volume })
	manifest.Chapters = manifest.Chapters[:0]
	for _, chapter := range chapterByKey {
		manifest.Chapters = append(manifest.Chapters, chapter)
	}
	sort.Slice(manifest.Chapters, func(i, j int) bool {
		if manifest.Chapters[i].Number == manifest.Chapters[j].Number {
			return manifest.Chapters[i].RawID < manifest.Chapters[j].RawID
		}
		return manifest.Chapters[i].Number < manifest.Chapters[j].Number
	})
	if err := validatePackManifest(manifest); err != nil {
		return err
	}
	if err := store.WriteJSONAtomic(PackManifestPath(root), manifest); err != nil {
		return err
	}
	return syncDirectory(root)
}

// RecordRecoveredPackManifest persists verified Wikipedia mappings against local legacy
// chapter directories without inventing Komiku URL or raw-ID provenance.
func RecordRecoveredPackManifest(seriesDir, series string, mappings []komiku.Volume, sources []PackChapterSource) error {
	transaction, err := prepareRecoveredPackManifest(seriesDir, series, mappings, sources)
	if err != nil {
		return err
	}
	defer transaction.Abort()
	return transaction.Commit()
}

type recoveredManifestTransaction struct {
	root              string
	manifest          PackManifest
	release           func()
	syncDirectory     func(string) error
	removeManifest    func(string) error
	manifestPublished bool
	closed            bool
}

func prepareRecoveredPackManifest(seriesDir, series string, mappings []komiku.Volume, sources []PackChapterSource) (*recoveredManifestTransaction, error) {
	root, err := canonicalSeriesRoot(seriesDir)
	if err != nil {
		return nil, err
	}
	release, err := acquireManifestLock(root)
	if err != nil {
		return nil, err
	}
	transaction := &recoveredManifestTransaction{root: root, release: release, syncDirectory: syncDirectory, removeManifest: os.Remove}
	manifest, err := loadPackManifestIfPresent(root)
	if err != nil {
		transaction.Abort()
		return nil, err
	}
	if manifest.Version == 0 {
		manifest = PackManifest{Version: PackManifestVersion, Series: series}
	}
	if manifest.Series != series {
		transaction.Abort()
		return nil, fmt.Errorf("pack manifest series conflict: %q != %q", manifest.Series, series)
	}
	mappingByVolume := make(map[int]PackManifestMapping, len(manifest.Mappings)+len(mappings))
	for _, mapping := range manifest.Mappings {
		mappingByVolume[mapping.Volume] = mapping
	}
	for _, mapping := range mappings {
		incoming := PackManifestMapping{Volume: mapping.Volume, Start: mapping.Start, End: mapping.End, Provenance: "wikipedia-recovery"}
		if existing, exists := mappingByVolume[mapping.Volume]; exists {
			if existing.Volume != incoming.Volume || existing.Start != incoming.Start || existing.End != incoming.End {
				transaction.Abort()
				return nil, fmt.Errorf("pack manifest volume %02d mapping conflicts with existing data", mapping.Volume)
			}
			continue
		}
		mappingByVolume[mapping.Volume] = incoming
	}
	chapterByKey := make(map[string]PackManifestChapter, len(manifest.Chapters)+len(sources))
	for _, chapter := range manifest.Chapters {
		chapterByKey[manifestChapterKey(chapter.URL, chapter.RawID, chapter.Number)] = chapter
	}
	for _, source := range sources {
		if !source.Complete || source.ExpectedPages <= 0 || source.Volume <= 0 || source.Chapter.URL != "" || source.Chapter.RawID != "" {
			transaction.Abort()
			return nil, fmt.Errorf("recovered chapter %s lacks trusted local completion metadata", source.Chapter.Display)
		}
		relative, pages, err := validatePackSource(root, source.Dir)
		if err != nil {
			transaction.Abort()
			return nil, fmt.Errorf("recovered chapter %s source: %w", source.Chapter.Display, err)
		}
		if pages != source.ExpectedPages {
			transaction.Abort()
			return nil, fmt.Errorf("recovered chapter %s has %d of %d expected pages", source.Chapter.Display, pages, source.ExpectedPages)
		}
		incoming := PackManifestChapter{IdentityProvenance: "recovered-local", Display: source.Chapter.Display, Number: source.Chapter.Number, Volume: source.Volume, SourceDir: filepath.ToSlash(relative), ExpectedPages: source.ExpectedPages, Completed: true}
		key := manifestChapterKey("", "", incoming.Number)
		if existing, exists := chapterByKey[key]; exists && existing != incoming {
			transaction.Abort()
			return nil, fmt.Errorf("pack manifest recovered chapter %s conflicts with existing data", source.Chapter.Display)
		}
		chapterByKey[key] = incoming
	}
	manifest.Mappings = manifest.Mappings[:0]
	for _, mapping := range mappingByVolume {
		manifest.Mappings = append(manifest.Mappings, mapping)
	}
	sort.Slice(manifest.Mappings, func(i, j int) bool { return manifest.Mappings[i].Volume < manifest.Mappings[j].Volume })
	manifest.Chapters = manifest.Chapters[:0]
	for _, chapter := range chapterByKey {
		manifest.Chapters = append(manifest.Chapters, chapter)
	}
	sort.Slice(manifest.Chapters, func(i, j int) bool { return manifest.Chapters[i].Number < manifest.Chapters[j].Number })
	if err := validatePackManifest(manifest); err != nil {
		transaction.Abort()
		return nil, err
	}
	transaction.manifest = manifest
	return transaction, nil
}

func (transaction *recoveredManifestTransaction) Commit() error {
	if transaction == nil || transaction.closed {
		return errors.New("recovered manifest transaction is closed")
	}
	defer transaction.Abort()
	manifestPath := PackManifestPath(transaction.root)
	if err := store.WriteJSONAtomic(manifestPath, transaction.manifest); err != nil {
		return err
	}
	transaction.manifestPublished = true
	if err := transaction.syncDirectory(transaction.root); err != nil {
		removeErr := transaction.removeManifest(manifestPath)
		if removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
			transaction.manifestPublished = false
			removeErr = transaction.syncDirectory(transaction.root)
		}
		return errors.Join(err, removeErr)
	}
	return nil
}

func (transaction *recoveredManifestTransaction) HasPublishedManifest() bool {
	return transaction != nil && transaction.manifestPublished
}

func (transaction *recoveredManifestTransaction) Abort() {
	if transaction == nil || transaction.closed {
		return
	}
	transaction.closed = true
	transaction.release()
}

func LoadPackManifest(seriesDir string) (PackManifest, error) {
	root, err := canonicalSeriesRoot(seriesDir)
	if err != nil {
		return PackManifest{}, err
	}
	manifest, err := loadPackManifestIfPresent(root)
	if err != nil {
		return PackManifest{}, err
	}
	if manifest.Version == 0 {
		return PackManifest{}, fmt.Errorf("%w at %s", errPackManifestNotFound, PackManifestPath(root))
	}
	if err := validatePackManifest(manifest); err != nil {
		return PackManifest{}, err
	}
	return manifest, nil
}

func PrepareManifestPack(seriesDir string, preset packer.Preset, volumeExpression string) (PackPlan, error) {
	root, err := canonicalSeriesRoot(seriesDir)
	if err != nil {
		return PackPlan{}, err
	}
	manifest, err := LoadPackManifest(seriesDir)
	if err != nil {
		return PackPlan{}, err
	}
	mappings := make([]komiku.Volume, 0, len(manifest.Mappings))
	for _, mapping := range manifest.Mappings {
		mappings = append(mappings, komiku.Volume{Volume: mapping.Volume, Start: mapping.Start, End: mapping.End})
	}
	explicit := strings.TrimSpace(volumeExpression) != ""
	if explicit {
		mappings, err = SelectedVolumeMappings(mappings, volumeExpression)
		if err != nil {
			return PackPlan{}, err
		}
	}
	wanted := make(map[int]bool, len(mappings))
	for _, mapping := range mappings {
		wanted[mapping.Volume] = true
	}
	sources := make([]PackChapterSource, 0, len(manifest.Chapters))
	for _, chapter := range manifest.Chapters {
		if !wanted[chapter.Volume] {
			continue
		}
		relative := filepath.FromSlash(chapter.SourceDir)
		cleanRelative, pages, err := validatePackSource(root, relative)
		if err != nil {
			return PackPlan{}, fmt.Errorf("manifest chapter %s source: %w", chapter.Display, err)
		}
		if filepath.Clean(cleanRelative) != filepath.Clean(relative) {
			return PackPlan{}, fmt.Errorf("manifest chapter %s source path is not canonical", chapter.Display)
		}
		sources = append(sources, PackChapterSource{Chapter: komiku.Chapter{URL: chapter.URL, RawID: chapter.RawID, Display: chapter.Display, Number: chapter.Number}, Volume: chapter.Volume, Dir: cleanRelative, ExpectedPages: chapter.ExpectedPages, Complete: chapter.Completed && pages == chapter.ExpectedPages})
	}
	plan := preparePackSources(root, root, manifest.Series, preset, mappings, sources)
	if len(plan.Skipped) > 0 {
		suffix := ""
		if !explicit {
			suffix = "; use --vol to select only complete declared volumes"
		}
		return PackPlan{}, fmt.Errorf("volume %02d cannot be packed: %s%s", plan.Skipped[0].Volume, plan.Skipped[0].Reason, suffix)
	}
	if len(plan.Volumes) == 0 {
		return PackPlan{}, errors.New("pack manifest has no complete declared volumes")
	}
	return plan, nil
}

func canonicalSeriesRoot(seriesDir string) (string, error) {
	if seriesDir == "" {
		return "", errors.New("series directory is empty")
	}
	info, err := os.Stat(seriesDir)
	if err != nil {
		return "", fmt.Errorf("open series directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("series path is not a directory: %s", seriesDir)
	}
	root, err := filepath.EvalSymlinks(seriesDir)
	if err != nil {
		return "", fmt.Errorf("resolve series directory: %w", err)
	}
	return filepath.Clean(root), nil
}

func lstatRealRootPath(root *os.Root, relative string) (os.FileInfo, error) {
	clean := filepath.Clean(relative)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, errors.New("source path escapes series root")
	}
	parts := strings.Split(clean, string(filepath.Separator))
	current := ""
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, errors.New("source path is malformed")
		}
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("source path component %s is a symlink", current)
		}
		if index < len(parts)-1 && !info.IsDir() {
			return nil, fmt.Errorf("source path component %s is not a directory", current)
		}
		if index == len(parts)-1 {
			return info, nil
		}
	}
	return nil, errors.New("source path is empty")
}

func validatePackSource(root, source string) (string, int, error) {
	relative, err := sourceRelativeToRoot(root, source)
	if err != nil {
		return "", 0, err
	}
	confined, err := os.OpenRoot(root)
	if err != nil {
		return "", 0, fmt.Errorf("open series root: %w", err)
	}
	defer confined.Close()
	info, err := lstatRealRootPath(confined, relative)
	if err != nil {
		return "", 0, fmt.Errorf("inspect source directory: %w", err)
	}
	if !info.IsDir() {
		return "", 0, errors.New("source directory must be a real directory, not a symlink")
	}
	dir, err := confined.Open(relative)
	if err != nil {
		return "", 0, fmt.Errorf("open source directory: %w", err)
	}
	defer dir.Close()
	openedInfo, err := dir.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return "", 0, errors.New("source directory changed while opening")
	}
	pages := make(map[int]bool)
	for {
		entries, readErr := dir.ReadDir(256)
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 {
				return "", 0, fmt.Errorf("source entry %s is a symlink", entry.Name())
			}
			if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			extension := strings.ToLower(filepath.Ext(entry.Name()))
			if extension != ".jpg" && extension != ".jpeg" && extension != ".png" && extension != ".webp" {
				continue
			}
			number, parseErr := strconv.Atoi(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())))
			if parseErr != nil || number <= 0 {
				continue
			}
			if number > store.MaxChapterPages || len(pages) >= store.MaxChapterPages {
				return "", 0, fmt.Errorf("source page number exceeds limit %d", store.MaxChapterPages)
			}
			if pages[number] {
				return "", 0, fmt.Errorf("duplicate source page %03d", number)
			}
			pages[number] = true
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", 0, fmt.Errorf("read source directory: %w", readErr)
		}
	}
	if len(pages) == 0 {
		return "", 0, errors.New("chapter has no valid image pages")
	}
	for number := 1; number <= len(pages); number++ {
		if !pages[number] {
			return "", 0, fmt.Errorf("chapter is missing page %03d", number)
		}
	}
	return relative, len(pages), nil
}

func sourceRelativeToRoot(root, source string) (string, error) {
	if source == "" || strings.ContainsAny(source, "\x00\\") {
		return "", errors.New("source directory is empty or malformed")
	}
	var relative string
	if filepath.IsAbs(source) {
		providedInfo, err := os.Lstat(filepath.Clean(source))
		if err != nil {
			return "", fmt.Errorf("inspect source directory: %w", err)
		}
		if providedInfo.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("source directory is a symlink")
		}
		resolved, err := filepath.EvalSymlinks(filepath.Clean(source))
		if err != nil {
			return "", fmt.Errorf("resolve source directory: %w", err)
		}
		relative, err = filepath.Rel(root, resolved)
		if err != nil {
			return "", fmt.Errorf("resolve source directory: %w", err)
		}
	} else {
		relative = filepath.Clean(filepath.FromSlash(source))
	}
	if relative == "." || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative != filepath.Clean(relative) {
		return "", fmt.Errorf("source directory escapes series root: %s", source)
	}
	return relative, nil
}

var manifestProcessLocks sync.Map

func acquireManifestLock(seriesDir string) (func(), error) {
	processLockValue, _ := manifestProcessLocks.LoadOrStore(seriesDir, &sync.Mutex{})
	processLock := processLockValue.(*sync.Mutex)
	processLock.Lock()
	processLocked := true
	defer func() {
		if processLocked {
			processLock.Unlock()
		}
	}()

	const lockName = ".pack.json.lock"
	root, err := os.OpenRoot(seriesDir)
	if err != nil {
		return nil, fmt.Errorf("open pack manifest lock root: %w", err)
	}
	before, inspectErr := root.Lstat(lockName)
	if inspectErr == nil && (before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular()) {
		root.Close()
		return nil, errors.New("pack manifest lock must be a regular non-symlink file")
	}
	if inspectErr != nil && !errors.Is(inspectErr, os.ErrNotExist) {
		root.Close()
		return nil, fmt.Errorf("inspect pack manifest lock: %w", inspectErr)
	}
	file, err := root.OpenFile(lockName, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("open pack manifest lock: %w", err)
	}
	opened, statErr := file.Stat()
	current, currentErr := root.Lstat(lockName)
	if statErr != nil || currentErr != nil || current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || !os.SameFile(current, opened) || (before != nil && !os.SameFile(before, opened)) {
		file.Close()
		root.Close()
		return nil, errors.New("pack manifest lock changed while opening")
	}
	unlock, err := lockManifestFile(file)
	if err != nil {
		file.Close()
		root.Close()
		return nil, err
	}
	processLocked = false
	return func() {
		unlock()
		_ = file.Close()
		_ = root.Close()
		processLock.Unlock()
	}, nil
}

const (
	maxPackManifestBytes    int64 = 4 << 20
	maxPackManifestMappings       = 1000
	maxPackManifestChapters       = 10_000
	maxPackChapterNumber          = 1_000_000
)

func loadPackManifestIfPresent(seriesDir string) (PackManifest, error) {
	confined, err := os.OpenRoot(seriesDir)
	if err != nil {
		return PackManifest{}, fmt.Errorf("open pack manifest root: %w", err)
	}
	defer confined.Close()
	info, err := confined.Lstat(".pack.json")
	if errors.Is(err, os.ErrNotExist) {
		return PackManifest{}, nil
	}
	if err != nil {
		return PackManifest{}, fmt.Errorf("inspect pack manifest: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return PackManifest{}, errors.New("pack manifest must be a regular non-symlink file")
	}
	if info.Size() > maxPackManifestBytes {
		return PackManifest{}, fmt.Errorf("pack manifest exceeds byte limit %d", maxPackManifestBytes)
	}
	file, err := confined.Open(".pack.json")
	if err != nil {
		return PackManifest{}, fmt.Errorf("read pack manifest: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return PackManifest{}, errors.New("pack manifest changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxPackManifestBytes+1))
	if err != nil {
		return PackManifest{}, fmt.Errorf("read pack manifest: %w", err)
	}
	if int64(len(data)) > maxPackManifestBytes {
		return PackManifest{}, fmt.Errorf("pack manifest exceeds byte limit %d", maxPackManifestBytes)
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return PackManifest{}, fmt.Errorf("invalid pack manifest JSON: %w", err)
	}
	var manifest PackManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return PackManifest{}, fmt.Errorf("invalid pack manifest JSON: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return PackManifest{}, fmt.Errorf("invalid trailing pack manifest JSON: %w", err)
	}
	if manifest.Version != 0 {
		if err := validatePackManifest(manifest); err != nil {
			return PackManifest{}, err
		}
	}
	return manifest, nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := validateJSONValue(decoder); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func validateJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if seen[key] {
				return fmt.Errorf("duplicate object key %q", key)
			}
			seen[key] = true
			if err := validateJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("unterminated JSON object")
		}
	case '[':
		for decoder.More() {
			if err := validateJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("unterminated JSON array")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func validatePackManifest(manifest PackManifest) error {
	if manifest.Version != PackManifestVersion {
		return fmt.Errorf("unsupported pack manifest version %d", manifest.Version)
	}
	if err := validateManifestString("series", manifest.Series, 200, true); err != nil {
		return err
	}
	if manifest.SeriesURL != "" {
		if len(manifest.SeriesURL) > 2048 {
			return errors.New("pack manifest series URL is too long")
		}
		if _, err := komiku.ValidateSeriesURL(manifest.SeriesURL); err != nil {
			return fmt.Errorf("pack manifest series URL: %w", err)
		}
	}
	if len(manifest.Mappings) == 0 || len(manifest.Mappings) > maxPackManifestMappings {
		return fmt.Errorf("pack manifest mapping count must be between 1 and %d", maxPackManifestMappings)
	}
	if len(manifest.Chapters) > maxPackManifestChapters {
		return fmt.Errorf("pack manifest chapter count exceeds %d", maxPackManifestChapters)
	}
	rows := append([]PackManifestMapping(nil), manifest.Mappings...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Start < rows[j].Start })
	mappingByVolume := make(map[int]PackManifestMapping, len(rows))
	totalCoverage := 0
	for index, mapping := range rows {
		if mapping.Volume <= 0 || mapping.Volume > maxSelectedVolume || mapping.Start <= 0 || mapping.End < mapping.Start || mapping.End > maxPackChapterNumber || mapping.End-mapping.Start+1 > store.MaxChapterPages {
			return fmt.Errorf("pack manifest volume %02d has an invalid bounded integer range", mapping.Volume)
		}
		width := mapping.End - mapping.Start + 1
		if width > maxPackManifestChapters-totalCoverage {
			return fmt.Errorf("pack manifest mappings cover more than %d chapters", maxPackManifestChapters)
		}
		totalCoverage += width
		if mapping.Provenance != "wikipedia-display" && mapping.Provenance != "wikipedia-recovery" && mapping.Provenance != "manual-range" && mapping.Provenance != "download-mapping" {
			return fmt.Errorf("pack manifest volume %02d has invalid provenance %q", mapping.Volume, mapping.Provenance)
		}
		if _, duplicate := mappingByVolume[mapping.Volume]; duplicate {
			return fmt.Errorf("pack manifest volume %02d is duplicated", mapping.Volume)
		}
		mappingByVolume[mapping.Volume] = mapping
		if index > 0 && mapping.Start <= rows[index-1].End {
			return fmt.Errorf("pack manifest volumes %02d and %02d overlap", rows[index-1].Volume, mapping.Volume)
		}
	}
	seenNumbers := make(map[int]bool, len(manifest.Chapters))
	seenSources := make(map[string]bool, len(manifest.Chapters))
	for _, chapter := range manifest.Chapters {
		if !chapter.Completed {
			return fmt.Errorf("pack manifest chapter %q is not completed", chapter.Display)
		}
		number, err := strconv.Atoi(chapter.Display)
		if err != nil || number <= 0 || float64(number) != chapter.Number {
			return fmt.Errorf("pack manifest chapter %q lacks an exact integer identity", chapter.Display)
		}
		if seenNumbers[number] {
			return fmt.Errorf("pack manifest chapter %d is duplicated", number)
		}
		seenNumbers[number] = true
		mapping, exists := mappingByVolume[chapter.Volume]
		if !exists || number < mapping.Start || number > mapping.End {
			return fmt.Errorf("pack manifest chapter %d does not belong to declared volume %02d", number, chapter.Volume)
		}
		if chapter.ExpectedPages <= 0 || chapter.ExpectedPages > store.MaxChapterPages {
			return fmt.Errorf("pack manifest chapter %d has invalid expected page count", number)
		}
		if err := validateManifestSourcePath(chapter.SourceDir); err != nil {
			return fmt.Errorf("pack manifest chapter %d source: %w", number, err)
		}
		if seenSources[chapter.SourceDir] {
			return fmt.Errorf("pack manifest source %s is claimed by multiple chapters", chapter.SourceDir)
		}
		seenSources[chapter.SourceDir] = true
		switch chapter.IdentityProvenance {
		case "komiku-download":
			if err := validateManifestString("chapter URL", chapter.URL, 2048, false); err != nil {
				return err
			}
			if err := validateManifestString("chapter raw ID", chapter.RawID, 512, false); err != nil {
				return err
			}
		case "recovered-local":
			if chapter.URL != "" || chapter.RawID != "" {
				return fmt.Errorf("recovered local chapter %d must not claim Komiku identity", number)
			}
		default:
			return fmt.Errorf("pack manifest chapter %d has invalid identity provenance %q", number, chapter.IdentityProvenance)
		}
	}
	return nil
}

func validateManifestString(label, value string, maximum int, safeLeaf bool) error {
	if value == "" || len(value) > maximum || value != strings.TrimSpace(value) || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("pack manifest has invalid %s %q", label, value)
	}
	if safeLeaf && strings.ContainsAny(value, "/\\") {
		return fmt.Errorf("pack manifest has invalid %s %q", label, value)
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("pack manifest has invalid %s %q", label, value)
		}
	}
	return nil
}

func validateManifestSourcePath(source string) error {
	if source == "" || len(source) > 1024 || strings.ContainsAny(source, "\x00\\") || filepath.IsAbs(source) {
		return errors.New("source path must be a bounded relative path")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(source)))
	if clean != source || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return errors.New("source path is not canonical or escapes series root")
	}
	return nil
}

func syncDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open manifest directory for sync: %w", err)
	}
	defer file.Close()
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync manifest directory: %w", err)
	}
	return nil
}

func manifestChapterKey(url, rawID string, number float64) string {
	if url != "" {
		return "url:" + url
	}
	return fmt.Sprintf("local:%s:%g", rawID, number)
}
