package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	packer "github.com/bangadam/komiku-cli/pack"
)

// prepareFlatPack plans a single CBZ from legacy flat chapter directories
// without a volume mapping, a .state.json, or any network access. The archive
// spans the local chapter range; missing chapters are tolerated and packed
// around, so it works for partially downloaded series.
func prepareFlatPack(seriesDir, seriesOverride string, preset packer.Preset) (PackPlan, error) {
	root, err := canonicalSeriesRoot(seriesDir)
	if err != nil {
		return PackPlan{}, err
	}
	if info, err := os.Lstat(PackManifestPath(root)); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return PackPlan{}, errors.New("existing pack manifest is not a regular non-symlink file")
		}
		return PackPlan{}, errors.New("pack manifest already exists; use the normal offline pack command")
	} else if !errors.Is(err, os.ErrNotExist) {
		return PackPlan{}, fmt.Errorf("inspect existing pack manifest: %w", err)
	}
	series := strings.TrimSpace(seriesOverride)
	if series == "" {
		series = filepath.Base(root)
	}
	if series == "" || series == "." || series == ".." || len(series) > 200 || series != strings.TrimSpace(series) || strings.ContainsAny(series, "/\\\x00") {
		return PackPlan{}, fmt.Errorf("invalid series name %q", series)
	}
	local, err := discoverLegacyFlatChapters(root)
	if err != nil {
		return PackPlan{}, err
	}
	numbers := make([]float64, 0, len(local))
	for number := range local {
		numbers = append(numbers, number)
	}
	sort.Float64s(numbers)
	chapters := make([]packer.Chapter, 0, len(numbers))
	for _, number := range numbers {
		chapter := local[number]
		relative, pages, err := validatePackSource(root, chapter.SourceDir)
		if err != nil {
			return PackPlan{}, fmt.Errorf("flat chapter %s source: %w", chapter.Display, err)
		}
		chapters = append(chapters, packer.Chapter{Number: number, Display: chapter.Display, Dir: relative, ExpectedPages: pages})
	}
	first, last := local[numbers[0]].Display, local[numbers[len(numbers)-1]].Display
	title := fmt.Sprintf("%s Chapters %s-%s", series, first, last)
	if len(chapters) == 1 {
		title = fmt.Sprintf("%s Chapter %s", series, first)
	}
	return PackPlan{Preset: preset, Volumes: []packer.Volume{{
		SeriesDir: root, SourceRoot: root, Series: series, Number: 1, Title: title, Chapters: chapters,
	}}}, nil
}
