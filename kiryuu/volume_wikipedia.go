// volume_wikipedia.go resolves Wikipedia article titles and fetches their volume tables.
package kiryuu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

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
