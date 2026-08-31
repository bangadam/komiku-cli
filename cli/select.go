package cli

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/bangadam/komiku-cli/komiku"
)

func SelectChapters(chapters []komiku.Chapter, expression string) ([]komiku.Chapter, error) {
	if strings.TrimSpace(expression) == "" {
		return nil, fmt.Errorf("--ch requires a chapter list or range")
	}
	selected := make(map[string]komiku.Chapter)
	for _, rawToken := range strings.Split(expression, ",") {
		token := strings.TrimSpace(rawToken)
		if token == "" {
			return nil, fmt.Errorf("invalid empty chapter selector in %q", expression)
		}
		if lo, hi, rangeOK := parseNumberRange(token); rangeOK {
			found := false
			for _, chapter := range chapters {
				if chapter.Number >= lo && chapter.Number <= hi {
					selected[chapter.URL] = chapter
					found = true
				}
			}
			if !found {
				return nil, fmt.Errorf("chapter range %s contains no discovered chapters", token)
			}
			continue
		}
		chapter, err := selectOneChapter(chapters, token)
		if err != nil {
			return nil, err
		}
		selected[chapter.URL] = chapter
	}
	result := make([]komiku.Chapter, 0, len(selected))
	for _, chapter := range selected {
		result = append(result, chapter)
	}
	if err := rejectAmbiguousSelection(result); err != nil {
		return nil, err
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Number == result[j].Number {
			return result[i].RawID < result[j].RawID
		}
		return result[i].Number < result[j].Number
	})
	return result, nil
}

func selectOneChapter(chapters []komiku.Chapter, token string) (komiku.Chapter, error) {
	for _, chapter := range chapters {
		if token == chapter.RawID {
			return chapter, nil
		}
	}
	display := strings.Replace(token, "-", ".", 1)
	var match *komiku.Chapter
	for i := range chapters {
		if display != chapters[i].Display {
			continue
		}
		if match != nil {
			return komiku.Chapter{}, fmt.Errorf("chapter %q is ambiguous; choose raw ID %q or %q", token, match.RawID, chapters[i].RawID)
		}
		match = &chapters[i]
	}
	if match == nil {
		return komiku.Chapter{}, fmt.Errorf("chapter %q was not found in series discovery", token)
	}
	return *match, nil
}

func parseNumberRange(token string) (float64, float64, bool) {
	separator := strings.IndexByte(token, '-')
	if separator <= 0 || separator == len(token)-1 || strings.Count(token, "-") != 1 {
		return 0, 0, false
	}
	lo, errLo := strconv.ParseFloat(token[:separator], 64)
	hi, errHi := strconv.ParseFloat(token[separator+1:], 64)
	if errLo != nil || errHi != nil || lo > hi {
		return 0, 0, false
	}
	return lo, hi, true
}

func SelectVolumes(chapters []komiku.Chapter, volumes []komiku.Volume, expression string) ([]Job, error) {
	wanted, err := parseIntegerSelection(expression)
	if err != nil {
		return nil, err
	}
	known := make(map[int]bool, len(volumes))
	for _, volume := range volumes {
		known[volume.Volume] = true
	}
	for volume := range wanted {
		if !known[volume] {
			return nil, fmt.Errorf("volume %d is not present in mapping", volume)
		}
	}
	selectedChapters := make([]komiku.Chapter, 0, len(chapters))
	jobs := make([]Job, 0, len(chapters))
	for _, chapter := range chapters {
		volume, ok := komiku.VolumeForChapter(chapter.Number, volumes)
		if ok && wanted[volume] {
			jobs = append(jobs, Job{Chapter: chapter, Volume: volume})
			selectedChapters = append(selectedChapters, chapter)
		}
	}
	if err := rejectAmbiguousSelection(selectedChapters); err != nil {
		return nil, err
	}
	return jobs, nil
}

func rejectAmbiguousSelection(chapters []komiku.Chapter) error {
	seen := make(map[float64]string, len(chapters))
	for _, chapter := range chapters {
		if raw, exists := seen[chapter.Number]; exists && raw != chapter.RawID {
			return fmt.Errorf("chapter display %q is ambiguous between raw IDs %q and %q; select exactly one raw ID", chapter.Display, raw, chapter.RawID)
		}
		seen[chapter.Number] = chapter.RawID
	}
	return nil
}

const maxSelectedVolume = 1000

func parseIntegerSelection(expression string) (map[int]bool, error) {
	if strings.TrimSpace(expression) == "" {
		return nil, fmt.Errorf("volume selection is empty")
	}
	selected := make(map[int]bool)
	for _, rawToken := range strings.Split(expression, ",") {
		token := strings.TrimSpace(rawToken)
		parts := strings.Split(token, "-")
		if len(parts) == 1 {
			value, err := strconv.Atoi(parts[0])
			if err != nil || value <= 0 || value > maxSelectedVolume {
				return nil, fmt.Errorf("invalid volume %q; volume must be between 1 and %d", token, maxSelectedVolume)
			}
			selected[value] = true
			continue
		}
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid volume range %q", token)
		}
		lo, errLo := strconv.Atoi(parts[0])
		hi, errHi := strconv.Atoi(parts[1])
		if errLo != nil || errHi != nil || lo <= 0 || lo > hi || hi > maxSelectedVolume || hi-lo+1 > maxSelectedVolume {
			return nil, fmt.Errorf("invalid volume range %q; range must stay within 1-%d", token, maxSelectedVolume)
		}
		for volume := lo; volume <= hi; volume++ {
			selected[volume] = true
		}
	}
	return selected, nil
}
