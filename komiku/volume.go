package komiku

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
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
	orderedListPattern      = regexp.MustCompile(`(?is)<ol([^>]*)>(.*?)</ol>`)
	olStartPattern          = regexp.MustCompile(`(?i)\bstart\s*=\s*["']?([0-9]+)`)
	liTagPattern            = regexp.MustCompile(`(?i)<li[\s>/]`)
	listItemPattern         = regexp.MustCompile(`(?is)<li(?:\s[^>]*)?>(.*?)</li>`)
	numberedItemPattern     = regexp.MustCompile(`^([0-9]+)[.)]`)
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

// stripNestedSections removes nested <section> blocks (e.g. "Chapters not yet
// in tankōbon format") from a Volumes section so their items cannot extend
// the last volume's chapter range.
func stripNestedSections(section []byte) []byte {
	tags := tagPattern.FindAllIndex(section, -1)
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
	text := html.UnescapeString(string(tagPattern.ReplaceAll(row, []byte(" "))))
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
		text := strings.TrimSpace(html.UnescapeString(string(tagPattern.ReplaceAll(match[1], []byte(" ")))))
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
			if isVolumesSectionLabel(parseAttrs(tag)["aria-labelledby"]) {
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

func (c *Client) FetchWikipediaDisplayVolumes(ctx context.Context, seriesName string) ([]Volume, error) {
	name := strings.Join(strings.Fields(seriesName), "_")
	if name == "" {
		return nil, errors.New("series name is empty")
	}
	tried := make(map[string]bool)
	// Direct lookups cover titles whose Komiku slug matches the English
	// Wikipedia article name ("sakamoto-days"). Noisy slugs (Indonesian
	// suffixes, subtitles, hyphenation differences) 404 here and fall through
	// to the search resolution below.
	for _, suffix := range []string{"chapters", "volumes"} {
		volumes, found, err := c.fetchWikipediaDisplayPage(ctx, wikipediaArticleURL("List_of_"+name+"_"+suffix), tried)
		if err != nil {
			return nil, err
		}
		if found {
			return volumes, nil
		}
	}
	title, found, err := c.searchWikipediaListTitle(ctx, seriesName, tried)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	volumes, found, err := c.fetchWikipediaDisplayPage(ctx, wikipediaArticleURL(title), tried)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return volumes, nil
}

// FetchWikipediaDisplayVolumesStrict performs exactly one Wikipedia page
// fetch with no redirects and no title resolution; the pack-recovery command
// mandates this contract.
func (c *Client) FetchWikipediaDisplayVolumesStrict(ctx context.Context, seriesName string) ([]Volume, error) {
	name := strings.Join(strings.Fields(seriesName), "_")
	if name == "" {
		return nil, errors.New("series name is empty")
	}
	data, err := c.fetchHTML(ctx, wikipediaArticleURL("List_of_"+name+"_chapters"))
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

func (c *Client) fetchWikipediaDisplayPage(ctx context.Context, target string, tried map[string]bool) ([]Volume, bool, error) {
	if tried[target] {
		return nil, false, nil
	}
	data, status, statusText, err := c.fetchPage(ctx, target)
	if err != nil {
		return nil, false, fmt.Errorf("fetch Wikipedia volume groups: %w", err)
	}
	if status != http.StatusOK {
		return nil, false, nil
	}
	_ = statusText
	volumes, err := ParseWikipediaDisplayVolumes(data)
	if err != nil {
		if errors.Is(err, errNoWikipediaVolumeRows) {
			tried[target] = true
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("parse Wikipedia volume groups: %w", err)
	}
	return volumes, true, nil
}

func wikipediaArticleURL(title string) string {
	return "https://en.wikipedia.org/wiki/" + url.PathEscape(strings.ReplaceAll(title, " ", "_"))
}

// searchWikipediaListTitle resolves the English Wikipedia "List of ..."
// article for a series whose name (from the Komiku slug) does not match the
// article title. It phrase-searches progressively shortened prefixes of the
// name and picks a chapters/volumes list page, preferring "chapters" pages
// (they embed per-volume chapter lists) over "volumes" pages.
func (c *Client) searchWikipediaListTitle(ctx context.Context, seriesName string, tried map[string]bool) (string, bool, error) {
	words := strings.Fields(strings.NewReplacer("-", " ", "_", " ").Replace(seriesName))
	const maxSearches = 6
	for end := len(words); end >= 1; end-- {
		if len(words)-end >= maxSearches {
			break
		}
		title, found, err := c.searchWikipediaListTitleOnce(ctx, "List of "+strings.Join(words[:end], " "), tried)
		if err != nil {
			return "", false, err
		}
		if found {
			return title, true, nil
		}
	}
	return "", false, nil
}

func (c *Client) searchWikipediaListTitleOnce(ctx context.Context, phrase string, tried map[string]bool) (string, bool, error) {
	values := url.Values{}
	values.Set("action", "query")
	values.Set("list", "search")
	values.Set("format", "json")
	values.Set("srlimit", "20")
	values.Set("srsearch", `"`+phrase+`"`)
	data, status, _, err := c.fetchPage(ctx, "https://en.wikipedia.org/w/api.php?"+values.Encode())
	if err != nil {
		return "", false, fmt.Errorf("search Wikipedia for %q: %w", phrase, err)
	}
	if status != http.StatusOK {
		return "", false, fmt.Errorf("search Wikipedia for %q: status %d", phrase, status)
	}
	var response struct {
		Query struct {
			Search []struct {
				Title string `json:"title"`
			} `json:"search"`
		} `json:"query"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return "", false, fmt.Errorf("search Wikipedia for %q: %w", phrase, err)
	}
	titles := make([]string, 0, len(response.Query.Search))
	for _, item := range response.Query.Search {
		titles = append(titles, item.Title)
	}
	title := selectWikipediaListTitle(titles, phrase, tried)
	if title == "" {
		return "", false, nil
	}
	return title, true, nil
}

// selectWikipediaListTitle picks the best chapters/volumes article from search
// results: "chapters" pages win over "volumes", candidates must share a word
// with the searched series, and pages fetched earlier are skipped.
func selectWikipediaListTitle(titles []string, phrase string, tried map[string]bool) string {
	shared := searchPhraseWords(phrase)
	for _, kind := range []string{"chapters", "volumes"} {
		for _, item := range titles {
			title := strings.TrimSpace(item)
			match := wikipediaListTitlePattern.FindStringSubmatch(title)
			if match == nil || !strings.EqualFold(match[2], kind) || !sharesSearchWord(title, shared) {
				continue
			}
			if tried[wikipediaArticleURL(title)] {
				continue
			}
			return title
		}
	}
	return ""
}

var wikipediaListTitlePattern = regexp.MustCompile(`(?i)^list of (.+) (chapters|volumes)$`)

func searchPhraseWords(phrase string) map[string]bool {
	words := map[string]bool{}
	for _, word := range strings.Fields(strings.ToLower(strings.NewReplacer("-", " ", "_", " ").Replace(phrase))) {
		if word == "list" || word == "of" {
			continue
		}
		words[word] = true
	}
	return words
}

func sharesSearchWord(title string, words map[string]bool) bool {
	replacer := strings.NewReplacer("-", " ", "_", " ", ":", " ", "'", "")
	for _, word := range strings.Fields(strings.ToLower(replacer.Replace(title))) {
		if words[word] {
			return true
		}
	}
	return false
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
