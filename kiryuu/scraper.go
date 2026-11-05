package kiryuu

import (
	"context"
	"fmt"
	"html"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	chapterImgTagPattern   = regexp.MustCompile(`(?is)<img\b[^>]*>`)
	chapterAnchorPattern   = regexp.MustCompile(`(?is)<a\b[^>]*>`)
	chapterAttrPattern     = regexp.MustCompile(`(?is)([a-zA-Z_:][-a-zA-Z0-9_:.]*)\s*=\s*["']([^"']*)["']`)
	chapterPathIDPattern   = regexp.MustCompile(`^/manga/[^/]+/chapter-[^/]+\.([0-9]+)/?$`)
	chapterLinkPathPattern = regexp.MustCompile(`^/manga/([^/]+)/chapter-([0-9]+(?:[.-][0-9]+)?)\.([0-9]+)/?$`)
)

// Chapter is one chapter of a series. RawID is the chapter post ID the Kiryuu
// REST API uses for direct lookup; URL is the canonical reader link.
type Chapter struct {
	RawID   string
	Display string
	Href    string
	URL     string
	Number  float64
}

// scraperChapter is the shape the v1 chapter endpoint returns for one chapter.
type scraperChapter struct {
	ID      int    `json:"id"`
	Title   string `json:"title"`
	Number  string `json:"number"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

// Discover lists every chapter of a series by parsing its HTML page.
//
// The REST chapter list cannot be used for this: GET
// /wp-json/kiru/v2/chapter?parent_id=<id> returns only the latest 100 chapters
// and ignores every paging parameter (page, paged, per_page and offset all
// return the same 134 KB payload), while the series page embeds the complete
// list — 395 chapter anchors for Black Clover's 393 chapters.
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

// ExtractChapters collects the chapter links of a series page. Links live on
// the series' own /manga/<slug>/chapter-<N>.<postID>/ path; the number may be
// dotted (chapter-246.5.751646) and one dashed-decimal spelling is tolerated.
// The last path segment is always the chapter post ID.
func ExtractChapters(data []byte, seriesURL string) ([]Chapter, error) {
	base, err := url.Parse(seriesURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("invalid series URL %q", seriesURL)
	}
	slug := strings.Trim(strings.TrimPrefix(base.Path, "/manga/"), "/")

	anchors := chapterAnchorPattern.FindAll(data, -1)
	chapters := make([]Chapter, 0, len(anchors))
	seen := make(map[string]struct{}, len(anchors))
	for _, anchor := range anchors {
		href := html.UnescapeString(strings.TrimSpace(chapterAttrs(anchor)["href"]))
		if href == "" {
			continue
		}
		parsed, err := url.Parse(href)
		if err != nil {
			continue
		}
		resolved := base.ResolveReference(parsed)
		if !strings.EqualFold(resolved.Host, base.Host) {
			continue
		}
		parts := chapterLinkPathPattern.FindStringSubmatch(resolved.Path)
		if parts == nil || (slug != "" && !strings.EqualFold(parts[1], slug)) {
			continue
		}
		key := resolved.String()
		if _, exists := seen[key]; exists {
			continue
		}
		number, err := strconv.ParseFloat(strings.Replace(parts[2], "-", ".", 1), 64)
		if err != nil {
			continue
		}
		seen[key] = struct{}{}
		chapters = append(chapters, Chapter{
			RawID:   parts[3],
			Display: strconv.FormatFloat(number, 'f', -1, 64),
			Href:    href,
			URL:     key,
			Number:  number,
		})
	}
	if len(chapters) == 0 {
		return nil, fmt.Errorf("no chapters found for series %s", seriesURL)
	}
	sort.SliceStable(chapters, func(i, j int) bool {
		if chapters[i].Number != chapters[j].Number {
			return chapters[i].Number < chapters[j].Number
		}
		return chapters[i].RawID < chapters[j].RawID
	})
	return chapters, nil
}

// ChapterImages lists the page URLs of one chapter. Both reader links and
// legacy /manga/<series>/chapter-<N>.<postID>/ links are accepted. The REST
// call anchors to the chapter URL's own origin, not the configured base URL:
// a chapter link carries its host (production or a loopback fixture), and the
// REST endpoint lives on that same host.
func (c *Client) ChapterImages(ctx context.Context, chapterURL string) ([]string, error) {
	parsed, err := url.Parse(chapterURL)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("invalid chapter URL %q", chapterURL)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("invalid chapter URL %q", chapterURL)
	}
	var query url.Values
	if slug := strings.TrimSpace(parsed.Query().Get("chapter")); slug != "" {
		query = url.Values{"slug": {slug}}
	} else if match := chapterPathIDPattern.FindStringSubmatch(parsed.Path); match != nil {
		query = url.Values{"id": {match[1]}}
	} else {
		return nil, fmt.Errorf("unsupported chapter URL %q", chapterURL)
	}

	apiBase := url.URL{Scheme: scheme, Host: parsed.Host}
	target := apiBase.JoinPath("/wp-json/kiru/v1/chapter")
	target.RawQuery = query.Encode()
	var chapter scraperChapter
	if err := c.fetchJSON(ctx, target.String(), &chapter); err != nil {
		return nil, err
	}
	images := ExtractImageURLs([]byte(chapter.Content))
	if len(images) == 0 {
		return nil, fmt.Errorf("no pages found for chapter %s", chapterURL)
	}
	return images, nil
}

// ExtractImageURLs collects chapter page URLs in document order. Kiryuu serves
// pages only from cdn.kiryuu.io; reader ads come from other hosts and are
// dropped. Loopback hosts are accepted so the tests' fake HTTP servers can
// stand in for the CDN.
func ExtractImageURLs(data []byte) []string {
	tags := chapterImgTagPattern.FindAll(data, -1)
	urls := make([]string, 0, len(tags))
	for _, tag := range tags {
		attrs := chapterAttrs(tag)
		src := html.UnescapeString(strings.TrimSpace(attrs["src"]))
		if src == "" {
			continue
		}
		parsed, err := url.Parse(src)
		if err != nil || parsed.Host == "" || !isChapterPageHost(parsed) {
			continue
		}
		urls = append(urls, src)
	}
	return urls
}

func isChapterPageHost(parsed *url.URL) bool {
	host := strings.ToLower(parsed.Hostname())
	scheme := strings.ToLower(parsed.Scheme)
	if host == "cdn.kiryuu.io" {
		return scheme == "https"
	}
	if host == "localhost" {
		return scheme == "http" || scheme == "https"
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() && (scheme == "http" || scheme == "https")
	}
	return false
}

func chapterAttrs(tag []byte) map[string]string {
	matches := chapterAttrPattern.FindAllSubmatch(tag, -1)
	attrs := make(map[string]string, len(matches))
	for _, match := range matches {
		name := strings.ToLower(string(match[1]))
		if _, exists := attrs[name]; !exists {
			attrs[name] = string(match[2])
		}
	}
	return attrs
}
