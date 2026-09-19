package cli

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bangadam/komiku-cli/komiku"
	packer "github.com/bangadam/komiku-cli/pack"
)

// prepareFlatPack plans a single CBZ from legacy flat chapter directories
// without a volume mapping, a .state.json, or any network access. The archive
// spans the contiguous local chapter range, so it works for chapters that no
// published volume covers yet.
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
	for _, number := range numbers {
		if number != math.Trunc(number) {
			return PackPlan{}, fmt.Errorf("flat pack requires integer chapters; chapter %s has no volume mapping and cannot be grouped", formatRecoveredNumber(number))
		}
	}
	for index := 1; index < len(numbers); index++ {
		if numbers[index] != numbers[index-1]+1 {
			return PackPlan{}, fmt.Errorf("flat chapters are not contiguous: chapter %s is missing between %s and %s", formatRecoveredNumber(numbers[index-1]+1), formatRecoveredNumber(numbers[index-1]), formatRecoveredNumber(numbers[index]))
		}
	}
	start, end := int(numbers[0]), int(numbers[len(numbers)-1])
	sources := make([]PackChapterSource, 0, len(numbers))
	for _, number := range numbers {
		chapter := local[number]
		relative, pages, err := validatePackSource(root, chapter.SourceDir)
		if err != nil {
			return PackPlan{}, fmt.Errorf("flat chapter %s source: %w", chapter.Display, err)
		}
		sources = append(sources, PackChapterSource{Chapter: komiku.Chapter{Display: chapter.Display, Number: number}, Volume: 1, Dir: relative, ExpectedPages: pages, Complete: true})
	}
	plan := preparePackSources(root, root, series, preset, []komiku.Volume{{Volume: 1, Start: start, End: end}}, sources)
	if len(plan.Skipped) > 0 {
		return PackPlan{}, fmt.Errorf("flat range %d-%d cannot be packed: %s", start, end, plan.Skipped[0].Reason)
	}
	if plan.DisabledReason != "" || len(plan.Volumes) == 0 {
		if plan.DisabledReason == "" {
			plan.DisabledReason = "No flat chapter is complete."
		}
		return PackPlan{}, errors.New(plan.DisabledReason)
	}
	first, last := local[numbers[0]].Display, local[numbers[len(numbers)-1]].Display
	title := fmt.Sprintf("%s Chapters %s-%s", series, first, last)
	if start == end {
		title = fmt.Sprintf("%s Chapter %s", series, first)
	}
	plan.Volumes[0].Title = title
	return plan, nil
}
