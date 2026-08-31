package cli

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
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

func requiredChapterVolume(number float64, mappings []komiku.Volume) int {
	if number <= 0 || number != math.Trunc(number) {
		return 0
	}
	for _, mapping := range mappings {
		if number >= float64(mapping.Start) && number <= float64(mapping.End) {
			return mapping.Volume
		}
	}
	return 0
}

func PreparePack(seriesStore *store.SeriesStore, series string, preset packer.Preset, mappings []komiku.Volume, jobs []Job, results []Result) PackPlan {
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
	seenNumbers := make(map[int]bool, required)
	seenSources := make(map[string]bool, required)
	for _, source := range sources {
		number := source.Chapter.Number
		if number <= 0 || number != math.Trunc(number) || number < float64(mapping.Start) || number > float64(mapping.End) {
			return fmt.Errorf("chapter %s is outside the declared integer range", source.Chapter.Display)
		}
		display, err := strconv.Atoi(source.Chapter.Display)
		if err != nil || float64(display) != number {
			return fmt.Errorf("chapter %s does not have a canonical integer identity", source.Chapter.Display)
		}
		integer := int(number)
		if seenNumbers[integer] {
			return fmt.Errorf("chapter %d is duplicated", integer)
		}
		seenNumbers[integer] = true
		if source.Dir != "" {
			clean := filepath.Clean(source.Dir)
			if seenSources[clean] {
				return fmt.Errorf("source directory %s is claimed more than once", source.Dir)
			}
			seenSources[clean] = true
		}
	}
	for number := mapping.Start; number <= mapping.End; number++ {
		if !seenNumbers[number] {
			return fmt.Errorf("chapter %d is missing", number)
		}
	}
	if len(sources) != required {
		return fmt.Errorf("requires %d chapters, found %d", required, len(sources))
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
