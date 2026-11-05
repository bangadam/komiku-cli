// pack_manifest.go defines the .pack.json manifest model and records manifests from pack results.
package cli

import (
	"errors"
	"fmt"
	"github.com/bangadam/komiku-cli/kiryuu"
	packer "github.com/bangadam/komiku-cli/pack"
	"github.com/bangadam/komiku-cli/store"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

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

func RecordPackManifest(seriesDir, series, seriesURL, provenance string, mappings []kiryuu.Volume, jobs []Job, results []Result) error {
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
	wholePresent := make(map[int]bool, len(jobs))
	for _, job := range jobs {
		if job.Chapter.Number > 0 && job.Chapter.Number == math.Trunc(job.Chapter.Number) {
			wholePresent[int(job.Chapter.Number)] = true
		}
	}
	for _, job := range jobs {
		requiredVolume := requiredChapterVolume(job.Chapter.Number, mappings)
		if requiredVolume == 0 || job.Volume != requiredVolume {
			continue
		}
		// A fractional chapter that merely supplements an existing whole
		// chapter (2.5 next to 2) is an extra, not part of the volume.
		if job.Chapter.Number != math.Trunc(job.Chapter.Number) && wholePresent[int(math.Trunc(job.Chapter.Number))] {
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
// chapter directories without inventing Kiryuu URL or raw-ID provenance.
func RecordRecoveredPackManifest(seriesDir, series string, mappings []kiryuu.Volume, sources []PackChapterSource) error {
	transaction, err := prepareRecoveredPackManifestWithProvenance(seriesDir, series, "wikipedia-recovery", mappings, sources)
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

func prepareRecoveredPackManifest(seriesDir, series string, mappings []kiryuu.Volume, sources []PackChapterSource) (*recoveredManifestTransaction, error) {
	return prepareRecoveredPackManifestWithProvenance(seriesDir, series, "wikipedia-recovery", mappings, sources)
}

func prepareRecoveredPackManifestWithProvenance(seriesDir, series, provenance string, mappings []kiryuu.Volume, sources []PackChapterSource) (*recoveredManifestTransaction, error) {
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
		incoming := PackManifestMapping{Volume: mapping.Volume, Start: mapping.Start, End: mapping.End, Provenance: provenance}
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
	manifestNumbers := make(map[float64]bool, len(manifest.Chapters))
	for _, chapter := range manifest.Chapters {
		chapterByKey[manifestChapterKey(chapter.URL, chapter.RawID, chapter.Number)] = chapter
		manifestNumbers[chapter.Number] = true
	}
	for _, source := range sources {
		if !source.Complete || source.ExpectedPages <= 0 || source.Volume <= 0 || source.Chapter.URL != "" || source.Chapter.RawID != "" {
			transaction.Abort()
			return nil, fmt.Errorf("recovered chapter %s lacks trusted local completion metadata", source.Chapter.Display)
		}
		// A chapter already recorded from a Kiryuu download keeps its original
		// identity; the local source is only added for chapters the manifest
		// does not know yet.
		if manifestNumbers[source.Chapter.Number] {
			continue
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

func PrepareManifestPack(seriesDir string, preset packer.Preset, volumeExpression string) (PackPlan, error) {
	root, err := canonicalSeriesRoot(seriesDir)
	if err != nil {
		return PackPlan{}, err
	}
	manifest, err := LoadPackManifest(seriesDir)
	if err != nil {
		return PackPlan{}, err
	}
	mappings := make([]kiryuu.Volume, 0, len(manifest.Mappings))
	for _, mapping := range manifest.Mappings {
		mappings = append(mappings, kiryuu.Volume{Volume: mapping.Volume, Start: mapping.Start, End: mapping.End})
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
		sources = append(sources, PackChapterSource{Chapter: kiryuu.Chapter{URL: chapter.URL, RawID: chapter.RawID, Display: chapter.Display, Number: chapter.Number}, Volume: chapter.Volume, Dir: cleanRelative, ExpectedPages: chapter.ExpectedPages, Complete: chapter.Completed && pages == chapter.ExpectedPages})
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
