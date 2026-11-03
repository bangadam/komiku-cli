// pack_manifest_selection.go expands complete volume selections into pack jobs.
package cli

import (
	"errors"
	"fmt"
	"github.com/bangadam/komiku-cli/komiku"
	"github.com/bangadam/komiku-cli/store"
	"math"
	"sort"
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
