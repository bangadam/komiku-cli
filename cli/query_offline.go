package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/bangadam/komiku-cli/komiku"
)

// localSeriesInfo builds a series report from disk alone. The GUI shows this
// before packing so the volume list and the pack request share one source of
// truth and no lookup runs twice.
func localSeriesInfo(outputRoot, series, seriesURL string, includeVolumes bool) (SeriesInfo, error) {
	seriesDir := filepath.Join(outputRoot, series)
	info := SeriesInfo{Series: series, SeriesURL: seriesURL, Chapters: []ChapterInfo{}}
	if cover, ok := existingCover(seriesDir); ok {
		info.Cover = cover
	}

	local, err := discoverLocalChapters(seriesDir)
	if err != nil {
		if _, statErr := os.Stat(seriesDir); statErr != nil {
			return info, nil
		}
		if !errors.Is(err, errNoLocalChapters) {
			return SeriesInfo{}, err
		}
		local = map[float64]legacyFlatChapter{}
	}

	done, tracked, err := readDoneIfPresent(seriesDir)
	if err != nil {
		return SeriesInfo{}, err
	}

	numbers := make([]float64, 0, len(local))
	for number := range local {
		numbers = append(numbers, number)
	}
	sort.Float64s(numbers)
	for _, number := range numbers {
		chapter := local[number]
		// Without a state file every folder counts as complete, matching the
		// offline pack; page checks are the real guard there.
		isDone := done[number] || !tracked
		info.Chapters = append(info.Chapters, ChapterInfo{
			Display: chapter.Display,
			Number:  chapter.Number,
			Done:    isDone,
		})
		if isDone {
			info.DoneCount++
		}
	}

	if !includeVolumes {
		return info, nil
	}
	mappings, err := localVolumeMapping(seriesDir)
	if err != nil {
		info.VolumeError = err.Error()
		return info, nil
	}
	if len(mappings) == 0 {
		info.VolumeError = "no local volume mapping; pack this series as one file"
		return info, nil
	}
	info.Volumes = mappings
	return info, nil
}

// readDoneIfPresent reads .state.json. The boolean reports whether the file
// exists at all: a series downloaded before state tracking has none.
func readDoneIfPresent(seriesDir string) (map[float64]bool, bool, error) {
	if _, err := os.Lstat(filepath.Join(seriesDir, ".state.json")); errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	done, err := loadLegacyDoneState(seriesDir)
	if err != nil {
		return nil, false, err
	}
	return done, true, nil
}

// localVolumeMapping reads the mapping from disk and never goes online. The
// provider cache comes first because it holds the whole series mapping, while
// a manifest only records the volumes packed so far.
func localVolumeMapping(seriesDir string) ([]komiku.Volume, error) {
	cache, err := komiku.DecodeVolumeCacheFile(filepath.Join(seriesDir, ".volumes.json"))
	if err == nil && len(cache.Volumes) > 0 {
		return cache.Volumes, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	manifest, err := LoadPackManifest(seriesDir)
	if err == nil {
		mappings := make([]komiku.Volume, 0, len(manifest.Mappings))
		for _, mapping := range manifest.Mappings {
			mappings = append(mappings, komiku.Volume{Volume: mapping.Volume, Start: mapping.Start, End: mapping.End})
		}
		return mappings, nil
	}
	if !errors.Is(err, errPackManifestNotFound) {
		return nil, fmt.Errorf("read pack manifest: %w", err)
	}
	return nil, nil
}
