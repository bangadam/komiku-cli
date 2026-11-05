// volume.go defines the volume mapping model, its on-disk cache format, and the shared chapter-range parsing helpers.
package kiryuu

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
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

// ErrNoWikipediaVolumeRows reports that Wikipedia has no usable volume rows
// for a series; callers can fall back to chapter-range packing.
var ErrNoWikipediaVolumeRows = errors.New("Wikipedia has no usable volume grouping")

var errNoWikipediaVolumeRows = ErrNoWikipediaVolumeRows

var (
	volumeRecordPattern     = regexp.MustCompile(`(?is)VolumeNumber.{0,400}?["']?wt["']?\s*[:=]\s*["']?([0-9]+)["']?.{0,1000}?.{0,600}?\bstart\s*=\s*["']?([0-9]+)`)
	volumeJSONPattern       = regexp.MustCompile(`(?is)VolumeNumber.{0,400}?["']wt["']\s*:\s*["']([0-9]+)["'].{0,1000}?.{0,600}?\bstart=([0-9]+)`)
	volumeHeadingPattern    = regexp.MustCompile(`(?is)\bVolume\s+([0-9]+)\b.{0,1000}?.{0,600}?\bstart\s*=\s*["']?([0-9]+)`)
	wikipediaChapterPattern = regexp.MustCompile(`(?i)\bDays\s+([0-9]+)\s*[:.]`)
	orderedListPattern      = regexp.MustCompile(`(?is)<ol([^>]*)>(.*?)</ol>`)
	olStartPattern          = regexp.MustCompile(`(?i)\bstart\s*=\s*["']?([0-9]+)`)
	liTagPattern            = regexp.MustCompile(`(?i)<li[\s>/]`)
	listItemPattern         = regexp.MustCompile(`(?is)<li(?:\s[^>]*)?>(.*?)</li>`)
	numberedItemPattern     = regexp.MustCompile(`^([0-9]+)[.)]`)
	volumeTagPattern        = regexp.MustCompile(`(?is)<[^>]+>`)
	volumeAttrPattern       = regexp.MustCompile(`(?is)([a-zA-Z_:][-a-zA-Z0-9_:.]*)\s*=\s*["']([^"']*)["']`)
)

// volumeAttrs parses the attributes of one HTML tag.
func volumeAttrs(tag []byte) map[string]string {
	attrs := make(map[string]string, 4)
	for _, match := range volumeAttrPattern.FindAllSubmatch(tag, -1) {
		attrs[strings.ToLower(string(match[1]))] = string(match[2])
	}
	return attrs
}

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

// stripNestedSections removes nested <section> blocks (e.g. "Chapters not yet
// in tankōbon format") from a Volumes section so their items cannot extend
// the last volume's chapter range.
func stripNestedSections(section []byte) []byte {
	tags := volumeTagPattern.FindAllIndex(section, -1)
	if len(tags) == 0 {
		return section
	}
	stripped := make([]byte, 0, len(section))
	copied := 0
	depth := 0
	for _, bounds := range tags {
		tag := section[bounds[0]:bounds[1]]
		lower := strings.ToLower(strings.TrimSpace(string(tag)))
		switch {
		case strings.HasPrefix(lower, "<section"):
			if depth == 0 {
				stripped = append(stripped, section[copied:bounds[0]]...)
			}
			depth++
		case strings.HasPrefix(lower, "</section"):
			if depth > 0 {
				depth--
				if depth == 0 {
					copied = bounds[1]
				}
			}
		}
	}
	stripped = append(stripped, section[copied:]...)
	return stripped
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

// DecodeVolumeCacheFile reads a pinned .volumes.json mapping from disk. It is
// the offline path used when no provider lookup is allowed.
func DecodeVolumeCacheFile(path string) (VolumeCache, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return VolumeCache{}, err
	}
	return DecodeVolumeCache(data, 0)
}
