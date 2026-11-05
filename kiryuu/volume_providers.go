// volume_providers.go fetches per-chapter volume numbers from MangaDex and the community Fandom wiki.
package kiryuu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

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
