// volume_wikipedia_parse.go parses per-volume chapter rows out of English Wikipedia articles.
package kiryuu

import (
	"errors"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
)

func ParseWikipediaDisplayVolumes(data []byte) ([]Volume, error) {
	section, err := wikipediaVolumesSection(data)
	if err != nil {
		return nil, err
	}
	section = stripNestedSections(section)
	type marker struct {
		volume int
		start  int
		end    int
	}
	tags := volumeTagPattern.FindAllIndex(section, -1)
	markers := make([]marker, 0)
	for _, bounds := range tags {
		tag := section[bounds[0]:bounds[1]]
		lower := strings.ToLower(strings.TrimSpace(string(tag)))
		if !strings.HasPrefix(lower, "<th") || strings.HasPrefix(lower, "</") {
			continue
		}
		attrs := volumeAttrs(tag)
		id := strings.ToLower(strings.TrimSpace(attrs["id"]))
		if attrs["scope"] != "row" || !strings.HasPrefix(id, "vol") {
			continue
		}
		volume, parseErr := strconv.Atoi(strings.TrimPrefix(id, "vol"))
		if parseErr != nil || volume <= 0 {
			continue
		}
		markers = append(markers, marker{volume: volume, start: bounds[0], end: len(section)})
	}
	if len(markers) == 0 {
		return nil, errNoWikipediaVolumeRows
	}
	for index := 0; index+1 < len(markers); index++ {
		markers[index].end = markers[index+1].start
	}
	// Marker spans start at the volume's own <th> and end at the next volume's
	// <th>, which covers chapter lists rendered in a separate following row.
	// Extractors read the raw slice, so the leading <th> tag itself is harmless.

	volumes := make([]Volume, 0, len(markers))
	seenVolumes := make(map[int]struct{}, len(markers))
	usableRows := 0
	for _, marker := range markers {
		if _, exists := seenVolumes[marker.volume]; exists {
			return nil, fmt.Errorf("Wikipedia Volumes section repeats volume %d", marker.volume)
		}
		seenVolumes[marker.volume] = struct{}{}
		chapters, extractErr := wikipediaRowChapters(section[marker.start:marker.end])
		if extractErr != nil {
			return nil, fmt.Errorf("Wikipedia volume %d: %w", marker.volume, extractErr)
		}
		if len(chapters) == 0 {
			continue
		}
		usableRows++
		for index := 1; index < len(chapters); index++ {
			if chapters[index] != chapters[index-1]+1 {
				return nil, fmt.Errorf("Wikipedia volume %d chapter list is not contiguous at %d-%d", marker.volume, chapters[index-1], chapters[index])
			}
		}
		volumes = append(volumes, Volume{Volume: marker.volume, Start: chapters[0], End: chapters[len(chapters)-1]})
	}
	if usableRows == 0 {
		return nil, errNoWikipediaVolumeRows
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

// wikipediaRowChapters extracts the chapter numbers covered by one Wikipedia
// volume row. Wikipedia renders per-volume chapter lists in several shapes:
// explicit labels ("Days 1:"), numbered lists whose numbering is carried by
// <ol start="N"> attributes, and plain list items with a "N." number prefix.
// The first shape that yields any chapter is used.
func wikipediaRowChapters(row []byte) ([]int, error) {
	if chapters, err := wikipediaRowChapterLabels(row); err == nil && len(chapters) > 0 {
		return chapters, nil
	}
	if chapters := wikipediaRowOrderedLists(row); len(chapters) > 0 {
		return chapters, nil
	}
	if chapters := wikipediaRowNumberedItems(row); len(chapters) > 0 {
		return chapters, nil
	}
	return nil, nil
}

// wikipediaRowChapterLabels collects chapters from labeled entries such as
// "Days 1:" inside the row, in document order.
func wikipediaRowChapterLabels(row []byte) ([]int, error) {
	text := html.UnescapeString(string(volumeTagPattern.ReplaceAll(row, []byte(" "))))
	chapters := make([]int, 0)
	seen := make(map[int]struct{})
	for _, match := range wikipediaChapterPattern.FindAllStringSubmatch(text, -1) {
		chapter, err := strconv.Atoi(match[1])
		if err != nil || chapter <= 0 {
			continue
		}
		if _, exists := seen[chapter]; !exists {
			seen[chapter] = struct{}{}
			chapters = append(chapters, chapter)
		}
	}
	sort.Ints(chapters)
	return chapters, nil
}

// wikipediaRowOrderedLists collects chapters from <ol> groups where the first
// chapter number is carried by the start attribute and each <li> is one
// chapter, and from plain <ul> groups whose items carry a "N." number prefix.
func wikipediaRowOrderedLists(row []byte) []int {
	chapters := make([]int, 0)
	seen := make(map[int]struct{})
	next := 0
	for _, match := range orderedListPattern.FindAllSubmatch(row, -1) {
		start := 1
		if startAttr := olStartPattern.FindSubmatch(match[1]); startAttr != nil {
			parsed, err := strconv.Atoi(string(startAttr[1]))
			if err != nil || parsed <= 0 {
				continue
			}
			start = parsed
		} else if next > 0 {
			start = next
		}
		count := len(liTagPattern.FindAllIndex(match[2], -1))
		if count == 0 {
			continue
		}
		for chapter := start; chapter < start+count; chapter++ {
			if _, exists := seen[chapter]; !exists {
				seen[chapter] = struct{}{}
				chapters = append(chapters, chapter)
			}
		}
		next = start + count
	}
	sort.Ints(chapters)
	return chapters
}

// wikipediaRowNumberedItems collects chapters from list items whose text
// begins with an explicit number, such as `355. "One to Infinity"`.
func wikipediaRowNumberedItems(row []byte) []int {
	chapters := make([]int, 0)
	seen := make(map[int]struct{})
	for _, match := range listItemPattern.FindAllSubmatch(row, -1) {
		text := strings.TrimSpace(html.UnescapeString(string(volumeTagPattern.ReplaceAll(match[1], []byte(" ")))))
		label := numberedItemPattern.FindStringSubmatch(text)
		if label == nil {
			continue
		}
		chapter, err := strconv.Atoi(label[1])
		if err != nil || chapter <= 0 {
			continue
		}
		if _, exists := seen[chapter]; !exists {
			seen[chapter] = struct{}{}
			chapters = append(chapters, chapter)
		}
	}
	sort.Ints(chapters)
	return chapters
}

func wikipediaVolumesSection(data []byte) ([]byte, error) {
	tags := volumeTagPattern.FindAllIndex(data, -1)
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
			if isVolumesSectionLabel(volumeAttrs(tag)["aria-labelledby"]) {
				start, depth = bounds[1], 1
			}
		}
	}
	if start >= 0 {
		return nil, errors.New("Wikipedia Volumes section is incomplete")
	}
	return nil, errNoWikipediaVolumeRows
}

// isVolumesSectionLabel accepts the canonical "Volumes" heading id plus series
// namespaced variants such as "Blue_Lock_volumes". It rejects unrelated
// sections like "Chapters_not_yet_in_tankōbon_format" or "Volumes_28–48" only
// when they cannot be volume groupings... the suffix form must end with the
// word volumes itself.
func isVolumesSectionLabel(label string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(label), "_", " "))
	return normalized == "volumes" || strings.HasSuffix(normalized, " volumes")
}
