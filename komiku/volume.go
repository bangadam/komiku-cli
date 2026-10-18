package komiku

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
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

// VolumeSource selects which provider supplies the chapter→volume mapping.
type VolumeSource string

const (
	// VolumeSourceAuto tries MangaDex first, then Wikipedia, then Fandom.
	VolumeSourceAuto VolumeSource = "auto"
	// VolumeSourceMangaDex uses MangaDex only.
	VolumeSourceMangaDex VolumeSource = "mangadex"
	// VolumeSourceWikipedia uses Wikipedia only.
	VolumeSourceWikipedia VolumeSource = "wikipedia"
	// VolumeSourceFandom uses the community Fandom wiki only.
	VolumeSourceFandom VolumeSource = "fandom"
)

// ParseVolumeSource validates a --volume-source value.
func ParseVolumeSource(value string) (VolumeSource, error) {
	switch VolumeSource(strings.ToLower(strings.TrimSpace(value))) {
	case "", VolumeSourceAuto:
		return VolumeSourceAuto, nil
	case VolumeSourceMangaDex:
		return VolumeSourceMangaDex, nil
	case VolumeSourceWikipedia:
		return VolumeSourceWikipedia, nil
	case VolumeSourceFandom:
		return VolumeSourceFandom, nil
	default:
		return "", fmt.Errorf("unknown volume source %q; expected auto, mangadex, wikipedia, or fandom", value)
	}
}

// minVolumeCoverage is the fraction of the discovered chapter range a mapping
// must reach to be accepted in auto mode. MangaDex in particular often stops
// several volumes short, and a partial mapping silently drops later volumes.
const minVolumeCoverage = 0.9

// volumeSource is one candidate provider of a chapter→volume mapping.
type volumeSource struct {
	name  string
	fetch func(context.Context) ([]Volume, string, error)
}

// FetchVolumeMapping resolves a chapter→volume mapping from MangaDex, the
// default provider. Use FetchVolumeMappingFrom with VolumeSourceAuto to try
// Wikipedia and Fandom as fallbacks.
func (c *Client) FetchVolumeMapping(ctx context.Context, seriesName string, maxChapter int) (VolumeCache, error) {
	return c.FetchVolumeMappingFrom(ctx, seriesName, maxChapter, VolumeSourceMangaDex)
}

// FetchVolumeMappingFrom resolves a chapter→volume mapping from the requested
// provider. In auto mode a source whose mapping stops well short of the last
// discovered chapter is rejected and the next provider is tried.
func (c *Client) FetchVolumeMappingFrom(ctx context.Context, seriesName string, maxChapter int, source VolumeSource) (VolumeCache, error) {
	name := strings.TrimSpace(seriesName)
	if name == "" {
		return VolumeCache{}, errors.New("series name is empty")
	}

	attempts := make([]string, 0, 6)
	remember := func(label string, err error) {
		if err != nil {
			attempts = append(attempts, fmt.Sprintf("%s: %v", label, err))
		}
	}

	for _, candidate := range c.volumeSources(name, maxChapter, source, remember) {
		if err := ctx.Err(); err != nil {
			return VolumeCache{}, err
		}
		volumes, url, err := candidate.fetch(ctx)
		if err != nil {
			remember(candidate.name, err)
			continue
		}
		if len(volumes) == 0 {
			remember(candidate.name, errors.New("no volume grouping"))
			continue
		}
		if err := ValidateVolumes(volumes, maxChapter); err != nil {
			remember(candidate.name, err)
			continue
		}
		if source == VolumeSourceAuto && !coversChapterRange(volumes, maxChapter) {
			remember(candidate.name, fmt.Errorf("incomplete mapping: reaches chapter %d of %d", highestMappedChapter(volumes), maxChapter))
			continue
		}
		return VolumeCache{Source: url, Volumes: volumes}, nil
	}

	if len(attempts) == 0 {
		return VolumeCache{}, errors.New("automatic volume mapping failed: no source available")
	}
	return VolumeCache{}, fmt.Errorf(
		"automatic volume mapping failed (tried %d sources): %s\ncreate/edit .volumes.json or use --ch ... --flat",
		len(attempts),
		strings.Join(attempts, "; "),
	)
}

// coversChapterRange reports whether a mapping reaches far enough into the
// discovered chapter range to be trusted in auto mode.
func coversChapterRange(volumes []Volume, maxChapter int) bool {
	if maxChapter <= 0 {
		return true
	}
	return float64(highestMappedChapter(volumes)) >= float64(maxChapter)*minVolumeCoverage
}

func highestMappedChapter(volumes []Volume) int {
	highest := 0
	for _, volume := range volumes {
		if volume.End > highest {
			highest = volume.End
		}
	}
	return highest
}

// volumeSources builds the ordered provider list for a series name and the
// requested source selection.
func (c *Client) volumeSources(name string, maxChapter int, source VolumeSource, remember func(string, error)) []volumeSource {
	mangadex := volumeSource{
		name: "MangaDex",
		fetch: func(ctx context.Context) ([]Volume, string, error) {
			return c.fetchMangaDexVolumes(ctx, name, maxChapter)
		},
	}
	wikipedia := c.wikipediaVolumeSources(name, maxChapter, remember)
	fandom := volumeSource{
		name: "Fandom",
		fetch: func(ctx context.Context) ([]Volume, string, error) {
			return c.fetchFandomVolumes(ctx, name, maxChapter)
		},
	}

	switch source {
	case VolumeSourceMangaDex:
		return []volumeSource{mangadex}
	case VolumeSourceWikipedia:
		return wikipedia
	case VolumeSourceFandom:
		return []volumeSource{fandom}
	default:
		ordered := make([]volumeSource, 0, len(wikipedia)+2)
		ordered = append(ordered, mangadex)
		ordered = append(ordered, wikipedia...)
		ordered = append(ordered, fandom)
		return ordered
	}
}

// wikipediaVolumeSources builds the Wikipedia provider chain: list pages, the
// main article, then a search fallback for noisy slugs.
func (c *Client) wikipediaVolumeSources(name string, maxChapter int, remember func(string, error)) []volumeSource {
	sources := make([]volumeSource, 0, 3)
	for _, title := range []string{"List of " + name + " chapters", "List of " + name + " volumes", name} {
		title := title
		sources = append(sources, volumeSource{
			name: "Wikipedia " + title,
			fetch: func(ctx context.Context) ([]Volume, string, error) {
				return c.fetchWikipediaVolumes(ctx, title, maxChapter)
			},
		})
	}
	sources = append(sources, volumeSource{
		name: "Wikipedia search",
		fetch: func(ctx context.Context) ([]Volume, string, error) {
			return c.fetchWikipediaVolumesBySearch(ctx, name, maxChapter, remember)
		},
	})
	return sources
}

// fetchWikipediaVolumes reads one Wikipedia page and parses its volume table.
func (c *Client) fetchWikipediaVolumes(ctx context.Context, title string, maxChapter int) ([]Volume, string, error) {
	target := wikipediaArticleURL(title)
	data, status, _, err := c.fetchPage(ctx, target)
	if err != nil {
		return nil, target, err
	}
	if status != http.StatusOK {
		return nil, target, fmt.Errorf("status %d", status)
	}
	volumes, err := ParseWikipediaDisplayVolumes(data)
	if err != nil {
		return nil, target, err
	}
	return clampVolumes(volumes, maxChapter), target, nil
}

// fetchWikipediaVolumesBySearch resolves a noisy series name to a Wikipedia
// article through the search API, then parses that page.
func (c *Client) fetchWikipediaVolumesBySearch(ctx context.Context, name string, maxChapter int, remember func(string, error)) ([]Volume, string, error) {
	tried := make(map[string]bool)
	title, found, err := c.searchWikipediaListTitle(ctx, name, tried)
	if err != nil {
		return nil, "", err
	}
	if found {
		return c.fetchWikipediaVolumes(ctx, title, maxChapter)
	}

	// Fall back to a plain article search: many series keep their volume
	// table on the main article ("Versus (2022 manga)") rather than on a
	// "List of ..." page.
	for _, candidate := range c.searchWikipediaArticles(ctx, name) {
		volumes, url, err := c.fetchWikipediaVolumes(ctx, candidate, maxChapter)
		if err != nil {
			remember("Wikipedia search result "+candidate, err)
			continue
		}
		return volumes, url, nil
	}
	return nil, "", errors.New("no Wikipedia article with volume rows")
}

// searchWikipediaArticles returns article titles matching a series name.
func (c *Client) searchWikipediaArticles(ctx context.Context, name string) []string {
	values := url.Values{}
	values.Set("action", "query")
	values.Set("list", "search")
	values.Set("format", "json")
	values.Set("srlimit", "10")
	values.Set("srsearch", name+" manga")
	data, status, _, err := c.fetchPage(ctx, "https://en.wikipedia.org/w/api.php?"+values.Encode())
	if err != nil || status != http.StatusOK {
		return nil
	}
	var response struct {
		Query struct {
			Search []struct {
				Title string `json:"title"`
			} `json:"search"`
		} `json:"query"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil
	}
	titles := make([]string, 0, len(response.Query.Search))
	for _, item := range response.Query.Search {
		if strings.HasPrefix(strings.ToLower(item.Title), "list of ") {
			continue
		}
		titles = append(titles, item.Title)
	}
	return titles
}

// fetchMangaDexVolumes reads per-chapter volume numbers from the MangaDex
// aggregate endpoint. MangaDex coverage is uneven, so a partial mapping is
// accepted only when it yields contiguous, non-overlapping volume ranges.
func (c *Client) fetchMangaDexVolumes(ctx context.Context, name string, maxChapter int) ([]Volume, string, error) {
	id, err := c.mangaDexID(ctx, name)
	if err != nil {
		return nil, "", err
	}
	// No translatedLanguage filter: MangaDex spreads volume labels across
	// scanlation languages, and filtering to English drops most of them
	// (Dandadan returns 1 volume with the filter, 11 without).
	target := "https://api.mangadex.org/manga/" + id + "/aggregate"
	data, status, _, err := c.fetchPage(ctx, target)
	if err != nil {
		return nil, target, err
	}
	if status != http.StatusOK {
		return nil, target, fmt.Errorf("status %d", status)
	}

	var payload struct {
		Volumes map[string]struct {
			Chapters map[string]struct {
				Chapter string `json:"chapter"`
			} `json:"chapters"`
		} `json:"volumes"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, target, err
	}

	type bound struct{ start, end int }
	bounds := make(map[int]bound)
	for key, group := range payload.Volumes {
		if key == "none" {
			continue
		}
		volume, convErr := strconv.Atoi(key)
		if convErr != nil || volume <= 0 {
			continue
		}
		for _, chapter := range group.Chapters {
			whole := chapterNumber(chapter.Chapter)
			if whole <= 0 {
				continue
			}
			current, exists := bounds[volume]
			if !exists {
				bounds[volume] = bound{start: whole, end: whole}
				continue
			}
			if whole < current.start {
				current.start = whole
			}
			if whole > current.end {
				current.end = whole
			}
			bounds[volume] = current
		}
	}
	if len(bounds) == 0 {
		return nil, target, errors.New("no volume numbers in chapter feed")
	}

	numbers := make([]int, 0, len(bounds))
	for volume := range bounds {
		numbers = append(numbers, volume)
	}
	sort.Ints(numbers)
	volumes := make([]Volume, 0, len(numbers))
	for _, volume := range numbers {
		volumes = append(volumes, Volume{Volume: volume, Start: bounds[volume].start, End: bounds[volume].end})
	}
	if err := ValidateVolumes(volumes, maxChapter); err != nil {
		return nil, target, err
	}
	return volumes, target, nil
}

// mangaDexID resolves the MangaDex manga id for a series title.
func (c *Client) mangaDexID(ctx context.Context, name string) (string, error) {
	values := url.Values{}
	values.Set("title", name)
	values.Set("limit", "5")
	target := "https://api.mangadex.org/manga?" + values.Encode()
	data, status, _, err := c.fetchPage(ctx, target)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("MangaDex search status %d", status)
	}
	var payload struct {
		Data []struct {
			ID         string `json:"id"`
			Attributes struct {
				Title map[string]string `json:"title"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", err
	}
	want := normalizeTitle(name)
	for _, item := range payload.Data {
		for _, title := range item.Attributes.Title {
			if normalizeTitle(title) == want {
				return item.ID, nil
			}
		}
	}
	if len(payload.Data) > 0 {
		return payload.Data[0].ID, nil
	}
	return "", errors.New("series not found on MangaDex")
}

func normalizeTitle(value string) string {
	lower := strings.ToLower(value)
	var builder strings.Builder
	for _, r := range lower {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

// fetchFandomVolumes reads the community wiki "Volumes & Chapters" page.
func (c *Client) fetchFandomVolumes(ctx context.Context, name string, maxChapter int) ([]Volume, string, error) {
	slug := strings.ToLower(strings.ReplaceAll(name, " ", ""))
	target := "https://" + slug + ".fandom.com/wiki/Volumes_%26_Chapters"
	data, status, _, err := c.fetchPage(ctx, target)
	if err != nil {
		return nil, target, err
	}
	if status != http.StatusOK {
		return nil, target, fmt.Errorf("status %d", status)
	}
	volumes, err := ParseVolumeMapping(data, maxChapter)
	if err != nil {
		return nil, target, err
	}
	return clampVolumes(volumes, maxChapter), target, nil
}

// chapterNumber extracts the whole part of a chapter label ("12.5" → 12).
func chapterNumber(label string) int {
	label = strings.TrimSpace(label)
	if label == "" {
		return 0
	}
	if dot := strings.IndexByte(label, '.'); dot >= 0 {
		label = label[:dot]
	}
	value, err := strconv.Atoi(label)
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

// clampVolumes drops volumes that start beyond the last discovered chapter and
// caps the final range at maxChapter.
func clampVolumes(volumes []Volume, maxChapter int) []Volume {
	if maxChapter <= 0 {
		return volumes
	}
	kept := make([]Volume, 0, len(volumes))
	for _, volume := range volumes {
		if volume.Start > maxChapter {
			continue
		}
		if volume.End > maxChapter {
			volume.End = maxChapter
		}
		kept = append(kept, volume)
	}
	return kept
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
