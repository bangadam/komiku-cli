package kiryuu

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
)

var (
	cardAnchorPattern = regexp.MustCompile(`(?is)<a\b([^>]*)>(.*?)</a>`)
	cardHrefPattern   = regexp.MustCompile(`(?is)\bhref\s*=\s*["']([^"']*)["']`)
	cardAltPattern    = regexp.MustCompile(`(?is)\balt\s*=\s*["']([^"']*)["']`)
	cardTagPattern    = regexp.MustCompile(`(?is)<[^>]+>`)
	seriesPathPattern = regexp.MustCompile(`^/manga/([^/]+)/$`)
)

// Series is one title returned by a full-text search.
type Series struct {
	Title string
	Slug  string
	Href  string
	URL   string
}

// Search runs Kiryuu's advanced search and returns the matching series. The
// site exposes the same form-driven endpoint to anonymous clients, so no
// nonce or session is required.
func (c *Client) Search(ctx context.Context, query string) ([]Series, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("search query is empty")
	}
	target, err := c.ajaxURL("advanced_search")
	if err != nil {
		return nil, err
	}
	form := advancedSearchForm(1, "popular")
	form.Set("query", query)
	data, status, statusText, err := c.fetchPost(ctx, target, form)
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("POST %s: %s", target, statusText)
	}
	return ExtractSeriesResults(data, target)
}

// advancedSearchForm builds the pinned advanced_search body. Kiryuu requires
// every filter to be present; genre, status and type travel as JSON-array
// strings, so an empty filter is the literal "[]".
func advancedSearchForm(page int, orderby string) url.Values {
	if page < 1 {
		page = 1
	}
	form := url.Values{}
	form.Set("page", fmt.Sprint(page))
	form.Set("orderby", orderby)
	form.Set("query", "")
	form.Set("inclusion", "OR")
	form.Set("exclusion", "OR")
	form.Set("genre", "[]")
	form.Set("status", "[]")
	form.Set("type", "[]")
	form.Set("project", "0")
	return form
}

// jsonArray renders filter values as the JSON-array string the endpoint
// expects, for example ["action","comedy"].
func jsonArray(values ...string) string {
	kept := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			kept = append(kept, `"`+value+`"`)
		}
	}
	return "[" + strings.Join(kept, ",") + "]"
}

// seriesAnchor is one /manga/<slug>/ anchor found in a card or fragment.
type seriesAnchor struct {
	Href  string
	Title string
	Slug  string
	URL   string
}

// seriesAnchors collects the manga anchors of a fragment in document order.
// Relative hrefs resolve against base; non-manga anchors (chapter links,
// pagination, ads) are dropped.
func seriesAnchors(body string, base *url.URL) []seriesAnchor {
	var anchors []seriesAnchor
	for _, match := range cardAnchorPattern.FindAllStringSubmatch(body, -1) {
		href := cardFirstMatch(cardHrefPattern, match[1])
		raw := strings.TrimSpace(html.UnescapeString(href))
		if raw == "" {
			continue
		}
		ref, err := url.Parse(raw)
		if err != nil {
			continue
		}
		resolved := base.ResolveReference(ref)
		if resolved.Scheme != "http" && resolved.Scheme != "https" {
			continue
		}
		path := seriesPathPattern.FindStringSubmatch(resolved.Path)
		if path == nil {
			continue
		}
		anchors = append(anchors, seriesAnchor{
			Href:  raw,
			Title: anchorTitle(match[2]),
			Slug:  path[1],
			URL:   resolved.String(),
		})
	}
	return anchors
}

// anchorTitle prefers the anchor's visible text and falls back to the alt of
// an image inside it. The real card pairs a text-less cover link with a
// separate title link, so an empty text is normal rather than an error.
func anchorTitle(body string) string {
	if text := cardText(body); text != "" {
		return text
	}
	if alt := cardFirstMatch(cardAltPattern, body); alt != "" {
		return cardText(alt)
	}
	return ""
}

// ExtractSeriesResults parses a search fragment into one entry per slug.
// Duplicate anchors collapse into the first titled occurrence; anchors with
// neither text nor an image alt (the real card's text-less cover link is
// covered by its alt) carry no title and are skipped.
func ExtractSeriesResults(data []byte, baseURL string) ([]Series, error) {
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("invalid search base URL %q", baseURL)
	}
	anchors := seriesAnchors(string(data), base)
	results := make([]Series, 0, len(anchors))
	seen := make(map[string]bool, len(anchors))
	for _, anchor := range anchors {
		if anchor.Title == "" || seen[anchor.Slug] {
			continue
		}
		seen[anchor.Slug] = true
		results = append(results, Series{
			Title: anchor.Title,
			Slug:  anchor.Slug,
			Href:  anchor.Href,
			URL:   anchor.URL,
		})
	}
	return results, nil
}

// cardText strips tags, unescapes entities and collapses whitespace.
func cardText(raw string) string {
	text := cardTagPattern.ReplaceAllString(raw, " ")
	text = html.UnescapeString(text)
	return strings.Join(strings.Fields(text), " ")
}

func cardFirstMatch(pattern *regexp.Regexp, raw string) string {
	match := pattern.FindStringSubmatch(raw)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}
