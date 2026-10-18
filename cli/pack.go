package cli

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/bangadam/komiku-cli/komiku"
	packer "github.com/bangadam/komiku-cli/pack"
	"github.com/bangadam/komiku-cli/store"
)

type PackSkip struct {
	Volume int
	Reason string
}

type PackPlan struct {
	Preset         packer.Preset
	Volumes        []packer.Volume
	Skipped        []PackSkip
	DisabledReason string
}

type PackOutcome struct {
	Volume int
	Result packer.Result
	Err    error
}

// PackChapterSource keeps a logical volume assignment independent from its physical page directory.
type PackChapterSource struct {
	Chapter       komiku.Chapter
	Volume        int
	Dir           string
	ExpectedPages int
	Complete      bool
}

// requiredChapterVolume returns the volume a chapter belongs to. Komiku
// publishes some chapters as fractional parts (1.1, 1.2), so the whole part
// decides the volume and the fraction only orders it inside that volume.
func requiredChapterVolume(number float64, mappings []komiku.Volume) int {
	if number <= 0 {
		return 0
	}
	whole := math.Trunc(number)
	for _, mapping := range mappings {
		if whole >= float64(mapping.Start) && whole <= float64(mapping.End) {
			return mapping.Volume
		}
	}
	return 0
}

// dropFractionalExtras removes fractional chapters (2.5) that merely
// supplement a whole chapter (2) already present in the same volume. When a
// volume contains only fractional parts (Komiku splits 1 into 1.1 and 1.2),
// those parts are the volume's chapters and are kept.
func dropFractionalExtras[T any](items []T, number func(T) float64) []T {
	whole := make(map[int]bool, len(items))
	for _, item := range items {
		value := number(item)
		if value > 0 && value == math.Trunc(value) {
			whole[int(value)] = true
		}
	}
	kept := make([]T, 0, len(items))
	for _, item := range items {
		value := number(item)
		if value > 0 && value != math.Trunc(value) && whole[int(math.Trunc(value))] {
			continue
		}
		kept = append(kept, item)
	}
	return kept
}

func PreparePack(seriesStore *store.SeriesStore, series string, preset packer.Preset, mappings []komiku.Volume, jobs []Job, results []Result) PackPlan {
	// No volume mapping: fall back to one contiguous chapter-run archive
	// ("Series Chapters X-Y") so flat downloads remain packable.
	if len(mappings) == 0 {
		return prepareFlatRunPack(seriesStore, series, preset, jobs, results)
	}
	resultByRawID := make(map[string]Result, len(results))
	for _, result := range results {
		resultByRawID[result.Chapter.RawID] = result
	}
	sources := make([]PackChapterSource, 0, len(jobs))
	for _, job := range jobs {
		if requiredChapterVolume(job.Chapter.Number, mappings) != job.Volume {
			continue
		}
		result, exists := resultByRawID[job.Chapter.RawID]
		source := PackChapterSource{Chapter: job.Chapter, Volume: job.Volume}
		if exists {
			source.Complete = result.Status == Done
			source.ExpectedPages = result.Total
			source.Dir = result.SourceDir
		}
		if source.Dir == "" {
			rawDisambiguator := ""
			if job.Ambiguous {
				rawDisambiguator = job.Chapter.RawID
			}
			dir, err := seriesStore.ChapterDir(job.Chapter.Display, rawDisambiguator, job.Volume, false)
			if err == nil {
				source.Dir = dir
			}
		}
		sources = append(sources, source)
	}
	return PreparePackSources(seriesStore.Dir(), series, preset, mappings, sources)
}

func prepareFlatRunPack(seriesStore *store.SeriesStore, series string, preset packer.Preset, jobs []Job, results []Result) PackPlan {
	resultByRawID := make(map[string]Result, len(results))
	for _, result := range results {
		resultByRawID[result.Chapter.RawID] = result
	}
	plan := PackPlan{Preset: preset}
	numbers := make([]float64, 0, len(jobs))
	sources := make([]PackChapterSource, 0, len(jobs))
	for _, job := range jobs {
		number := job.Chapter.Number
		if number <= 0 || math.IsNaN(number) || math.IsInf(number, 0) || number > float64(maxPackChapterNumber) {
			plan.Skipped = append(plan.Skipped, PackSkip{Volume: 0, Reason: fmt.Sprintf("chapter %s is invalid", job.Chapter.Display)})
			continue
		}
		result, exists := resultByRawID[job.Chapter.RawID]
		source := PackChapterSource{Chapter: job.Chapter, Volume: 1}
		if exists {
			source.Complete = result.Status == Done
			source.ExpectedPages = result.Total
			source.Dir = result.SourceDir
		}
		if source.Dir == "" {
			rawDisambiguator := ""
			if job.Ambiguous {
				rawDisambiguator = job.Chapter.RawID
			}
			if dir, err := seriesStore.ChapterDir(job.Chapter.Display, rawDisambiguator, 1, true); err == nil {
				source.Dir = dir
			}
		}
		if !source.Complete {
			plan.Skipped = append(plan.Skipped, PackSkip{Volume: 0, Reason: fmt.Sprintf("chapter %s is not completely downloaded", job.Chapter.Display)})
			continue
		}
		sources = append(sources, source)
		numbers = append(numbers, number)
	}
	if len(plan.Skipped) > 0 || len(sources) == 0 {
		if len(plan.Skipped) == 0 {
			plan.Skipped = append(plan.Skipped, PackSkip{Volume: 0, Reason: "no flat chapter run can be packed"})
		}
		return plan
	}
	sort.Float64s(numbers)
	for index := 1; index < len(numbers); index++ {
		if numbers[index] <= numbers[index-1] {
			return PackPlan{Preset: preset, Skipped: []PackSkip{{Volume: 0, Reason: fmt.Sprintf("chapter %s is duplicated", formatPackNumber(numbers[index]))}}}
		}
	}
	for index := 1; index < len(numbers); index++ {
		previous, current := numbers[index-1], numbers[index]
		if math.Trunc(current)-math.Trunc(previous) >= 2 {
			plan.Skipped = append(plan.Skipped, PackSkip{Volume: 0, Reason: fmt.Sprintf(
				"chapter %s is missing between %s and %s",
				formatPackNumber(math.Trunc(previous)+1), formatPackNumber(previous), formatPackNumber(current))})
			return plan
		}
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Chapter.Number < sources[j].Chapter.Number })
	volume := packer.Volume{SeriesDir: seriesStore.Dir(), Series: series, Number: 1, Chapters: make([]packer.Chapter, 0, len(sources))}
	for _, source := range sources {
		volume.Chapters = append(volume.Chapters, packer.Chapter{Number: source.Chapter.Number, Display: source.Chapter.Display, Dir: source.Dir, ExpectedPages: source.ExpectedPages})
	}
	first, last := sources[0].Chapter.Display, sources[len(sources)-1].Chapter.Display
	if len(sources) == 1 {
		volume.Title = fmt.Sprintf("%s Chapter %s", series, first)
	} else {
		volume.Title = fmt.Sprintf("%s Chapters %s-%s", series, first, last)
	}
	plan.Volumes = append(plan.Volumes, volume)
	return plan
}

func formatPackNumber(number float64) string {
	return strconv.FormatFloat(number, 'f', -1, 64)
}

// PreparePackSources validates complete logical volumes while reading pages directly from
// the explicit source directories supplied by the caller.
func PreparePackSources(seriesDir, series string, preset packer.Preset, mappings []komiku.Volume, sources []PackChapterSource) PackPlan {
	return preparePackSources(seriesDir, "", series, preset, mappings, sources)
}

func preparePackSources(seriesDir, sourceRoot, series string, preset packer.Preset, mappings []komiku.Volume, sources []PackChapterSource) PackPlan {
	plan := PackPlan{Preset: preset}
	if len(mappings) == 0 {
		plan.DisabledReason = "Pack needs a volume mapping."
		return plan
	}
	sourcesByVolume := make(map[int][]PackChapterSource, len(mappings))
	for _, source := range sources {
		if requiredChapterVolume(source.Chapter.Number, mappings) != source.Volume {
			continue
		}
		sourcesByVolume[source.Volume] = append(sourcesByVolume[source.Volume], source)
	}
	for volume, volumeSources := range sourcesByVolume {
		sourcesByVolume[volume] = dropFractionalExtras(volumeSources, func(item PackChapterSource) float64 {
			return item.Chapter.Number
		})
	}
	seenVolumes := make(map[int]bool, len(mappings))
	for _, mapping := range mappings {
		volumeSources := sourcesByVolume[mapping.Volume]
		if seenVolumes[mapping.Volume] {
			plan.Skipped = append(plan.Skipped, PackSkip{Volume: mapping.Volume, Reason: "duplicate volume declaration"})
			continue
		}
		seenVolumes[mapping.Volume] = true
		if err := validatePackVolumeSources(mapping, volumeSources); err != nil {
			plan.Skipped = append(plan.Skipped, PackSkip{Volume: mapping.Volume, Reason: err.Error()})
			continue
		}
		chapters := make([]packer.Chapter, 0, len(volumeSources))
		var skipReason string
		for _, source := range volumeSources {
			if !source.Complete {
				skipReason = fmt.Sprintf("chapter %s is not completely downloaded", source.Chapter.Display)
				break
			}
			if source.Dir == "" {
				skipReason = fmt.Sprintf("chapter %s source directory is unavailable", source.Chapter.Display)
				break
			}
			if source.ExpectedPages > store.MaxChapterPages {
				skipReason = fmt.Sprintf("chapter %s exceeds page limit %d", source.Chapter.Display, store.MaxChapterPages)
				break
			}
			if sourceRoot == "" {
				pages, err := store.CountChapterPages(source.Dir)
				if err != nil {
					skipReason = fmt.Sprintf("chapter %s: %v", source.Chapter.Display, err)
					break
				}
				if pages != source.ExpectedPages {
					skipReason = fmt.Sprintf("chapter %s has %d of %d expected pages", source.Chapter.Display, pages, source.ExpectedPages)
					break
				}
			}
			if source.ExpectedPages <= 0 {
				skipReason = fmt.Sprintf("chapter %s expected page count is unavailable", source.Chapter.Display)
				break
			}
			pages := source.ExpectedPages
			chapters = append(chapters, packer.Chapter{Number: source.Chapter.Number, Display: source.Chapter.Display, Dir: source.Dir, ExpectedPages: pages})
		}
		if skipReason != "" {
			plan.Skipped = append(plan.Skipped, PackSkip{Volume: mapping.Volume, Reason: skipReason})
			continue
		}
		plan.Volumes = append(plan.Volumes, packer.Volume{SeriesDir: seriesDir, SourceRoot: sourceRoot, Series: series, Number: mapping.Volume, Chapters: chapters})
	}
	if len(plan.Volumes) == 0 {
		plan.DisabledReason = "No mapped volume has every required chapter and page."
	}
	return plan
}

func validatePackVolumeSources(mapping komiku.Volume, sources []PackChapterSource) error {
	if mapping.Volume <= 0 || mapping.Volume > maxSelectedVolume || mapping.Start <= 0 || mapping.End < mapping.Start || mapping.End > maxPackChapterNumber || mapping.End-mapping.Start+1 > store.MaxChapterPages {
		return errors.New("volume declaration must be a bounded positive integer range")
	}
	required := int(mapping.End - mapping.Start + 1)
	seenNumbers := make(map[float64]bool, required)
	seenSources := make(map[string]bool, required)
	// covered tracks which whole chapter numbers the volume actually has.
	// Komiku splits some chapters into fractional parts (1.1, 1.2), so a
	// volume declared as 1-4 may legitimately contain 1.1, 1.2, 2.1, 3, 4.
	covered := make(map[int]bool, required)
	for _, source := range sources {
		number := source.Chapter.Number
		if number <= 0 || number < float64(mapping.Start) || number > float64(mapping.End) {
			return fmt.Errorf("chapter %s is outside the declared range", source.Chapter.Display)
		}
		whole := int(math.Trunc(number))
		if whole < mapping.Start || whole > mapping.End {
			return fmt.Errorf("chapter %s is outside the declared range", source.Chapter.Display)
		}
		if seenNumbers[number] {
			return fmt.Errorf("chapter %s is duplicated", source.Chapter.Display)
		}
		seenNumbers[number] = true
		covered[whole] = true
		if source.Dir != "" {
			clean := filepath.Clean(source.Dir)
			if seenSources[clean] {
				return fmt.Errorf("source directory %s is claimed more than once", source.Dir)
			}
			seenSources[clean] = true
		}
	}
	for number := mapping.Start; number <= mapping.End; number++ {
		if !covered[number] {
			return fmt.Errorf("chapter %d is missing", number)
		}
	}
	return nil
}

func PackPreparedVolumes(ctx context.Context, plan PackPlan) ([]PackOutcome, error) {
	if plan.DisabledReason != "" {
		return nil, fmt.Errorf("pack disabled: %s", plan.DisabledReason)
	}
	outcomes := make([]PackOutcome, 0, len(plan.Volumes))
	for _, volume := range plan.Volumes {
		if err := ctx.Err(); err != nil {
			return outcomes, fmt.Errorf("pack interrupted: %w", err)
		}
		result, err := packer.PackVolume(ctx, volume, plan.Preset)
		outcomes = append(outcomes, PackOutcome{Volume: volume.Number, Result: result, Err: err})
		if err != nil && ctx.Err() != nil {
			return outcomes, fmt.Errorf("pack interrupted: %w", ctx.Err())
		}
	}
	return outcomes, nil
}
