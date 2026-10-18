package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/bangadam/komiku-cli/komiku"
)

// ParseVolumeMap parses the compact mapping the GUI already knows
// ("1:1-5,2:6-14,23:201-201") so a pack can run offline without a manifest.
func ParseVolumeMap(expression string) ([]komiku.Volume, error) {
	trimmed := strings.TrimSpace(expression)
	if trimmed == "" {
		return nil, errors.New("volume map is empty")
	}
	mappings := make([]komiku.Volume, 0, 16)
	for _, token := range strings.Split(trimmed, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			return nil, errors.New("volume map has an empty entry")
		}
		volumePart, rangePart, found := strings.Cut(token, ":")
		if !found {
			return nil, fmt.Errorf("volume map entry %q must look like VOLUME:START-END", token)
		}
		volume, err := strconv.Atoi(strings.TrimSpace(volumePart))
		if err != nil || volume <= 0 {
			return nil, fmt.Errorf("volume map entry %q has an invalid volume number", token)
		}
		startPart, endPart, found := strings.Cut(strings.TrimSpace(rangePart), "-")
		if !found {
			return nil, fmt.Errorf("volume map entry %q must look like VOLUME:START-END", token)
		}
		start, errStart := strconv.Atoi(strings.TrimSpace(startPart))
		end, errEnd := strconv.Atoi(strings.TrimSpace(endPart))
		if errStart != nil || errEnd != nil {
			return nil, fmt.Errorf("volume map entry %q has an invalid chapter range", token)
		}
		mappings = append(mappings, komiku.Volume{Volume: volume, Start: start, End: end})
	}
	if _, _, err := strictVolumeChapterIndex(mappings); err != nil {
		return nil, err
	}
	sort.Slice(mappings, func(i, j int) bool { return mappings[i].Volume < mappings[j].Volume })
	return mappings, nil
}

// localPackMapping reads a mapping already on disk so a pack with --vol never
// has to ask a provider again.
func localPackMapping(seriesDir string) ([]komiku.Volume, error) {
	root, err := canonicalSeriesRoot(seriesDir)
	if err != nil {
		return nil, err
	}
	return localVolumeMapping(root)
}

// packWithExplicitMapping packs using a mapping the caller already resolved.
// It never touches the network: the mapping and the local chapter folders are
// the only inputs.
func packWithExplicitMapping(ctx context.Context, stdout io.Writer, seriesDir string, options PackDownloadedOptions) error {
	root, err := canonicalSeriesRoot(seriesDir)
	if err != nil {
		return err
	}
	selected := options.Mappings
	if strings.TrimSpace(options.VolumeExpression) != "" {
		selected, err = SelectedVolumeMappings(selected, options.VolumeExpression)
		if err != nil {
			return err
		}
	}
	series := filepath.Base(root)

	local, err := discoverLocalChapters(root)
	if err != nil {
		return err
	}
	done, err := localDoneState(root, local)
	if err != nil {
		return err
	}
	sources, err := buildRecoverySources(root, selected, local, done)
	if err != nil {
		return err
	}
	plan := preparePackSources(root, root, series, options.Preset, selected, sources)
	if err := validatePackCommandPlan(plan); err != nil {
		return err
	}
	transaction, err := prepareRecoveredPackManifestWithProvenance(root, series, "download-mapping", selected, sources)
	if err != nil {
		return fmt.Errorf("prepare pack manifest: %w", err)
	}
	defer transaction.Abort()
	createdArchives, err := plannedPackArchives(plan)
	if err != nil {
		return err
	}
	if err := executePackCommand(ctx, stdout, plan); err != nil {
		return errors.Join(err, removeCreatedArchives(createdArchives))
	}
	if err := transaction.Commit(); err != nil {
		var cleanupErr error
		if !transaction.HasPublishedManifest() {
			cleanupErr = removeCreatedArchives(createdArchives)
		}
		return errors.Join(fmt.Errorf("save pack manifest: %w", err), cleanupErr)
	}
	fmt.Fprintf(stdout, "volume mapping: %s\n", formatVolumeMap(selected))
	fmt.Fprintf(stdout, "saved manifest: %s\n", PackManifestPath(root))
	return nil
}

// localDoneState reports which chapters are complete. A series without a
// .state.json still packs: every discovered folder is trusted once the page
// check in validatePackSource has run.
func localDoneState(root string, local map[float64]legacyFlatChapter) (map[float64]bool, error) {
	if _, err := os.Lstat(filepath.Join(root, ".state.json")); errors.Is(err, os.ErrNotExist) {
		done := make(map[float64]bool, len(local))
		for number := range local {
			done[number] = true
		}
		return done, nil
	} else if err != nil {
		return nil, fmt.Errorf("inspect legacy state: %w", err)
	}
	return loadLegacyDoneState(root)
}

func formatVolumeMap(mappings []komiku.Volume) string {
	parts := make([]string, 0, len(mappings))
	for _, mapping := range mappings {
		parts = append(parts, fmt.Sprintf("%d:%d-%d", mapping.Volume, mapping.Start, mapping.End))
	}
	return strings.Join(parts, ",")
}

// errNoLocalChapters reports a series directory without any chapter folders.
var errNoLocalChapters = errors.New("no canonical chapter directories were found")

// discoverLocalChapters indexes every canonical chapter folder in a series,
// both flat (chapter-001) and volume-scoped (vol-01/chapter-001).
func discoverLocalChapters(root string) (map[float64]legacyFlatChapter, error) {
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
	addChapter := func(relative string, info os.FileInfo) error {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("chapter %s is a symlink", relative)
		}
		if !info.IsDir() {
			return fmt.Errorf("chapter %s is not a directory", relative)
		}
		chapter, parseErr := parseLegacyFlatChapter(filepath.Base(relative))
		if parseErr != nil {
			return parseErr
		}
		chapter.SourceDir = filepath.ToSlash(relative)
		if previous, duplicate := chapters[chapter.Number]; duplicate {
			return fmt.Errorf("chapter %s duplicates %s", chapter.SourceDir, previous.SourceDir)
		}
		chapters[chapter.Number] = chapter
		return nil
	}

	for {
		entries, readErr := directory.ReadDir(256)
		entriesSeen += len(entries)
		if entriesSeen > maxPackManifestChapters*2 {
			return nil, fmt.Errorf("series directory entry count exceeds limit %d", maxPackManifestChapters*2)
		}
		for _, entry := range entries {
			name := entry.Name()
			switch {
			case strings.HasPrefix(name, "chapter-"):
				info, inspectErr := confined.Lstat(name)
				if inspectErr != nil {
					return nil, fmt.Errorf("inspect chapter %s: %w", name, inspectErr)
				}
				if err := addChapter(name, info); err != nil {
					return nil, err
				}
			case isVolumeDirName(name):
				info, inspectErr := confined.Lstat(name)
				if inspectErr != nil {
					return nil, fmt.Errorf("inspect volume directory %s: %w", name, inspectErr)
				}
				if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
					return nil, fmt.Errorf("volume directory %s is not a real directory", name)
				}
				volumeDir, openErr := confined.Open(name)
				if openErr != nil {
					return nil, fmt.Errorf("open volume directory %s: %w", name, openErr)
				}
				chapterEntries, readDirErr := volumeDir.ReadDir(-1)
				volumeDir.Close()
				if readDirErr != nil {
					return nil, fmt.Errorf("read volume directory %s: %w", name, readDirErr)
				}
				for _, chapterEntry := range chapterEntries {
					if !strings.HasPrefix(chapterEntry.Name(), "chapter-") {
						continue
					}
					relative := filepath.Join(name, chapterEntry.Name())
					chapterInfo, chapterErr := confined.Lstat(relative)
					if chapterErr != nil {
						return nil, fmt.Errorf("inspect chapter %s: %w", relative, chapterErr)
					}
					if err := addChapter(relative, chapterInfo); err != nil {
						return nil, err
					}
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("read series directory: %w", readErr)
		}
	}
	if len(chapters) == 0 {
		return nil, errNoLocalChapters
	}
	return chapters, nil
}
