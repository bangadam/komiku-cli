package komiku

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	hrefPattern        = regexp.MustCompile(`(?is)\bhref\s*=\s*["']([^"']+)["']`)
	chapterPathPattern = regexp.MustCompile(`^/(?:[^/?#]+)-chapter-([0-9]+(?:-[0-9]+)?)/?$`)
	imgPattern         = regexp.MustCompile(`(?is)<img\b[^>]*>`)
	attrPattern        = regexp.MustCompile(`(?is)([a-zA-Z_:][-a-zA-Z0-9_:.]*)\s*=\s*["']([^"']*)["']`)
)

type Chapter struct {
	RawID   string
	Display string
	Href    string
	URL     string
	Number  float64
}

func (c *Client) Discover(ctx context.Context, seriesURL string) ([]Chapter, error) {
	if _, err := ValidateSeriesURL(seriesURL); err != nil {
		return nil, err
	}
	data, err := c.fetchHTML(ctx, seriesURL)
	if err != nil {
		return nil, err
	}
	return ExtractChapters(data, seriesURL)
}

func ExtractChapters(data []byte, seriesURL string) ([]Chapter, error) {
	base, err := url.Parse(seriesURL)
	if err != nil {
		return nil, fmt.Errorf("invalid series URL: %w", err)
	}
	matches := hrefPattern.FindAllSubmatch(data, -1)
	chapters := make([]Chapter, 0, len(matches))
	seenHref := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		href := html.UnescapeString(string(match[1]))
		if !strings.HasPrefix(href, "/") || strings.HasPrefix(href, "//") {
			continue
		}
		u, err := url.Parse(href)
		if err != nil || u.IsAbs() || u.Host != "" {
			continue
		}
		parts := chapterPathPattern.FindStringSubmatch(u.Path)
		if parts == nil {
			continue
		}
		resolved := base.ResolveReference(u)
		key := resolved.String()
		if _, exists := seenHref[key]; exists {
			continue
		}
		seenHref[key] = struct{}{}
		raw := parts[1]
		number, err := strconv.ParseFloat(strings.Replace(raw, "-", ".", 1), 64)
		if err != nil {
			continue
		}
		chapters = append(chapters, Chapter{
			RawID:   raw,
			Display: formatRawID(raw),
			Href:    href,
			URL:     key,
			Number:  number,
		})
	}
	sort.SliceStable(chapters, func(i, j int) bool {
		if chapters[i].Number != chapters[j].Number {
			return chapters[i].Number < chapters[j].Number
		}
		return chapters[i].RawID < chapters[j].RawID
	})
	return chapters, nil
}

func formatRawID(raw string) string {
	parts := strings.SplitN(raw, "-", 2)
	whole := strings.TrimLeft(parts[0], "0")
	if whole == "" {
		whole = "0"
	}
	if len(parts) == 1 {
		return whole
	}
	fraction := strings.TrimRight(parts[1], "0")
	if fraction == "" {
		fraction = "0"
	}
	return whole + "." + fraction
}

func ExtractImageURLs(data []byte) []string {
	tags := imgPattern.FindAll(data, -1)
	primary := imageURLsForClass(tags, "klazy")
	if len(primary) > 0 {
		return primary
	}
	return imageURLsForClass(tags, "ww")
}

func imageURLsForClass(tags [][]byte, classToken string) []string {
	urls := make([]string, 0, len(tags))
	for _, tag := range tags {
		attrs := parseAttrs(tag)
		if !hasClassToken(attrs["class"], classToken) {
			continue
		}
		src := html.UnescapeString(strings.TrimSpace(attrs["src"]))
		if src == "" || strings.Contains(strings.ToLower(src), "thumbnail") {
			continue
		}
		if parsed, err := url.Parse(src); err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			continue
		}
		urls = append(urls, src)
	}
	return urls
}

func parseAttrs(tag []byte) map[string]string {
	matches := attrPattern.FindAllSubmatch(tag, -1)
	attrs := make(map[string]string, len(matches))
	for _, match := range matches {
		name := strings.ToLower(string(match[1]))
		if _, exists := attrs[name]; !exists {
			attrs[name] = string(match[2])
		}
	}
	return attrs
}

func hasClassToken(classes, wanted string) bool {
	for _, class := range strings.Fields(classes) {
		if class == wanted {
			return true
		}
	}
	return false
}
