package komiku

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Volume struct {
	Volume int `json:"volume"`
	Start  int `json:"start"`
	End    int `json:"end"`
}

type VolumeCache struct {
	Source  string   `json:"source"`
	Volumes []Volume `json:"volumes"`
}

var errNoWikipediaVolumeRows = errors.New("Wikipedia has no usable volume grouping")

var (
	volumeRecordPattern     = regexp.MustCompile(`(?is)VolumeNumber.{0,400}?["']?wt["']?\s*[:=]\s*["']?([0-9]+)["']?.{0,1000}?.{0,600}?\bstart\s*=\s*["']?([0-9]+)`)
	volumeJSONPattern       = regexp.MustCompile(`(?is)VolumeNumber.{0,400}?["']wt["']\s*:\s*["']([0-9]+)["'].{0,1000}?.{0,600}?\bstart=([0-9]+)`)
	volumeHeadingPattern    = regexp.MustCompile(`(?is)\bVolume\s+([0-9]+)\b.{0,1000}?.{0,600}?\bstart\s*=\s*["']?([0-9]+)`)
	wikipediaChapterPattern = regexp.MustCompile(`(?i)\bDays\s+([0-9]+)\s*[:.]`)
)

func volumeSection(data []byte) []byte {
	lower := strings.ToLower(string(data))
	start := -1
	for _, marker := range []string{`id="volumes"`, `id='volumes'`, `>volumes</h2>`} {
		if index := strings.Index(lower, marker); index >= 0 && (start < 0 || index < start) {
			start = index
		}
	}
	if start < 0 {
		return data
	}
	if end := strings.Index(lower[start+1:], "<h2"); end >= 0 {
		return data[start : start+1+end]
	}
	return data[start:]
}

func ParseVolumeMapping(data []byte, maxChapter int) ([]Volume, error) {
	data = volumeSection(data)
	pairs := volumeJSONPattern.FindAllSubmatch(data, -1)
	if len(pairs) == 0 {
		pairs = volumeRecordPattern.FindAllSubmatch(data, -1)
	}
	if len(pairs) == 0 {
		pairs = volumeHeadingPattern.FindAllSubmatch(data, -1)
	}
	starts := make([]Volume, 0, len(pairs))
	seen := make(map[int]bool, len(pairs))
	for _, pair := range pairs {
		volume, errVolume := strconv.Atoi(string(pair[1]))
		start, errStart := strconv.Atoi(string(pair[2]))
		if errVolume != nil || errStart != nil || volume <= 0 || start <= 0 || seen[volume] {
			continue
		}
		seen[volume] = true
		starts = append(starts, Volume{Volume: volume, Start: start})
	}
	if len(starts) == 0 {
		return nil, errors.New("no volume starts found")
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i].Volume < starts[j].Volume })
	for i := range starts {
		if i+1 < len(starts) {
			starts[i].End = starts[i+1].Start - 1
		} else {
			starts[i].End = maxChapter
		}
	}
	if err := ValidateVolumes(starts, maxChapter); err != nil {
		return nil, err
	}
	return starts, nil
}

func ValidateVolumes(volumes []Volume, maxChapter int) error {
	if len(volumes) == 0 {
		return errors.New("volume mapping is empty")
	}
	seen := make(map[int]bool, len(volumes))
	sorted := append([]Volume(nil), volumes...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Start == sorted[j].Start {
			return sorted[i].Volume < sorted[j].Volume
		}
		return sorted[i].Start < sorted[j].Start
	})
	for i, volume := range sorted {
		if volume.Volume <= 0 || volume.Start <= 0 || volume.End <= 0 {
			return fmt.Errorf("volume %d has non-positive number or range %d-%d", volume.Volume, volume.Start, volume.End)
		}
		if seen[volume.Volume] {
			return fmt.Errorf("duplicate volume %d", volume.Volume)
		}
		seen[volume.Volume] = true
		if volume.Start > volume.End {
			return fmt.Errorf("volume %d has reversed range %d-%d", volume.Volume, volume.Start, volume.End)
		}
		if i > 0 && volume.Start <= sorted[i-1].End {
			return fmt.Errorf("volume %d range %d-%d overlaps volume %d range %d-%d", volume.Volume, volume.Start, volume.End, sorted[i-1].Volume, sorted[i-1].Start, sorted[i-1].End)
		}
		if maxChapter > 0 && volume.Start > maxChapter {
			return fmt.Errorf("volume %d begins beyond discovered chapter %d", volume.Volume, maxChapter)
		}
	}
	return nil
}

func VolumeForChapter(number float64, volumes []Volume) (int, bool) {
	for _, volume := range volumes {
		if number >= float64(volume.Start) && number <= float64(volume.End)+0.999999 {
			return volume.Volume, true
		}
	}
	return 0, false
}

func MaxDiscoveredChapter(chapters []Chapter) int {
	maxChapter := 0
	for _, chapter := range chapters {
		whole, _ := strconv.Atoi(strings.SplitN(chapter.Display, ".", 2)[0])
		if whole > maxChapter {
			maxChapter = whole
		}
	}
	return maxChapter
}

func ParseWikipediaDisplayVolumes(data []byte) ([]Volume, error) {
	section, err := wikipediaVolumesSection(data)
	if err != nil {
		return nil, err
	}
	type marker struct {
		volume int
		start  int
		end    int
	}
	tags := tagPattern.FindAllIndex(section, -1)
	markers := make([]marker, 0)
	for _, bounds := range tags {
		tag := section[bounds[0]:bounds[1]]
		lower := strings.ToLower(strings.TrimSpace(string(tag)))
		if !strings.HasPrefix(lower, "<th") || strings.HasPrefix(lower, "</") {
			continue
		}
		attrs := parseAttrs(tag)
		id := strings.ToLower(strings.TrimSpace(attrs["id"]))
		if attrs["scope"] != "row" || !strings.HasPrefix(id, "vol") {
			continue
		}
		volume, parseErr := strconv.Atoi(strings.TrimPrefix(id, "vol"))
		if parseErr != nil || volume <= 0 {
			continue
		}
		markers = append(markers, marker{volume: volume, start: bounds[1], end: len(section)})
	}
	if len(markers) == 0 {
		return nil, errNoWikipediaVolumeRows
	}
	for index := 0; index+1 < len(markers); index++ {
		markers[index].end = markers[index+1].start
	}

	volumes := make([]Volume, 0, len(markers))
	seenVolumes := make(map[int]struct{}, len(markers))
	for _, marker := range markers {
		if _, exists := seenVolumes[marker.volume]; exists {
			return nil, fmt.Errorf("Wikipedia Volumes section repeats volume %d", marker.volume)
		}
		seenVolumes[marker.volume] = struct{}{}
		text := html.UnescapeString(string(tagPattern.ReplaceAll(section[marker.start:marker.end], []byte(" "))))
		matches := wikipediaChapterPattern.FindAllStringSubmatch(text, -1)
		chapters := make([]int, 0, len(matches))
		seenChapters := make(map[int]struct{}, len(matches))
		for _, match := range matches {
			chapter, parseErr := strconv.Atoi(match[1])
			if parseErr != nil || chapter <= 0 {
				continue
			}
			if _, exists := seenChapters[chapter]; !exists {
				seenChapters[chapter] = struct{}{}
				chapters = append(chapters, chapter)
			}
		}
		if len(chapters) == 0 {
			return nil, fmt.Errorf("Wikipedia volume %d has no chapter list", marker.volume)
		}
		sort.Ints(chapters)
		for index := 1; index < len(chapters); index++ {
			if chapters[index] != chapters[index-1]+1 {
				return nil, fmt.Errorf("Wikipedia volume %d chapter list is not contiguous at %d-%d", marker.volume, chapters[index-1], chapters[index])
			}
		}
		volumes = append(volumes, Volume{Volume: marker.volume, Start: chapters[0], End: chapters[len(chapters)-1]})
	}
	for index := 1; index < len(volumes); index++ {
		if volumes[index].Volume <= volumes[index-1].Volume {
			return nil, errors.New("Wikipedia volume rows are not in ascending order")
		}
		if volumes[index].Start <= volumes[index-1].End {
			return nil, fmt.Errorf("Wikipedia volume %d overlaps volume %d", volumes[index].Volume, volumes[index-1].Volume)
		}
	}
	if err := ValidateVolumes(volumes, 0); err != nil {
		return nil, fmt.Errorf("invalid Wikipedia volume rows: %w", err)
	}
	return volumes, nil
}

func wikipediaVolumesSection(data []byte) ([]byte, error) {
	tags := tagPattern.FindAllIndex(data, -1)
	start, depth := -1, 0
	for _, bounds := range tags {
		tag := data[bounds[0]:bounds[1]]
		lower := strings.ToLower(strings.TrimSpace(string(tag)))
		switch {
		case strings.HasPrefix(lower, "</section"):
			if start >= 0 {
				depth--
				if depth == 0 {
					return data[start:bounds[0]], nil
				}
			}
		case strings.HasPrefix(lower, "<section"):
			if start >= 0 {
				depth++
				continue
			}
			if strings.EqualFold(strings.TrimSpace(parseAttrs(tag)["aria-labelledby"]), "Volumes") {
				start, depth = bounds[1], 1
			}
		}
	}
	if start >= 0 {
		return nil, errors.New("Wikipedia Volumes section is incomplete")
	}
	return nil, errNoWikipediaVolumeRows
}

func (c *Client) FetchWikipediaDisplayVolumes(ctx context.Context, seriesName string) ([]Volume, error) {
	name := strings.Join(strings.Fields(seriesName), "_")
	if name == "" {
		return nil, errors.New("series name is empty")
	}
	wikiURL := "https://en.wikipedia.org/wiki/" + url.PathEscape("List_of_"+name+"_chapters")
	data, err := c.fetchHTML(ctx, wikiURL)
	if err != nil {
		return nil, fmt.Errorf("fetch Wikipedia volume groups: %w", err)
	}
	volumes, err := ParseWikipediaDisplayVolumes(data)
	if errors.Is(err, errNoWikipediaVolumeRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("parse Wikipedia volume groups: %w", err)
	}
	return volumes, nil
}

func (c *Client) FetchVolumeMapping(ctx context.Context, seriesName string, maxChapter int) (VolumeCache, error) {
	name := strings.TrimSpace(seriesName)
	if name == "" {
		return VolumeCache{}, errors.New("series name is empty")
	}
	wikiURL := "https://en.wikipedia.org/wiki/" + url.PathEscape("List of "+name+" chapters")
	if data, err := c.fetchHTML(ctx, wikiURL); err == nil {
		if volumes, parseErr := ParseVolumeMapping(data, maxChapter); parseErr == nil {
			return VolumeCache{Source: wikiURL, Volumes: volumes}, nil
		}
	}
	fandomSlug := strings.ToLower(strings.ReplaceAll(name, " ", ""))
	fandomURL := "https://" + fandomSlug + ".fandom.com/wiki/Volumes_%26Chapters"
	data, err := c.fetchHTML(ctx, fandomURL)
	if err != nil {
		return VolumeCache{}, fmt.Errorf("automatic volume mapping failed: Wikipedia and fandom unavailable: %w", err)
	}
	volumes, err := ParseVolumeMapping(data, maxChapter)
	if err != nil {
		return VolumeCache{}, fmt.Errorf("automatic volume mapping failed: fandom mapping invalid: %w", err)
	}
	return VolumeCache{Source: fandomURL, Volumes: volumes}, nil
}

func DecodeVolumeCache(data []byte, maxChapter int) (VolumeCache, error) {
	var cache VolumeCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return VolumeCache{}, fmt.Errorf("decode volume cache: %w", err)
	}
	if err := ValidateVolumes(cache.Volumes, maxChapter); err != nil {
		return VolumeCache{}, fmt.Errorf("invalid volume cache: %w", err)
	}
	return cache, nil
}
