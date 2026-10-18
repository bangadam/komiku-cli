package komiku

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// DiscoverySeries is one title on a discovery page (ranking, trending,
// catalogue, genre listing or the latest-chapter feed).
type DiscoverySeries struct {
	Title      string   `json:"title"`
	Slug       string   `json:"slug"`
	URL        string   `json:"url"`
	Cover      string   `json:"cover,omitempty"`
	Type       string   `json:"type,omitempty"`
	Genres     []string `json:"genres,omitempty"`
	Status     string   `json:"status,omitempty"`
	Rank       int      `json:"rank,omitempty"`
	Views      int64    `json:"views,omitempty"`
	Chapter    string   `json:"chapter,omitempty"`
	ChapterURL string   `json:"chapter_url,omitempty"`
	Updated    string   `json:"updated,omitempty"`
	Color      bool     `json:"color,omitempty"`
}

// AlphabetCount is one letter tile on the catalogue index.
type AlphabetCount struct {
	Letter string `json:"letter"`
	Count  int    `json:"count"`
}

// CatalogReport is the paginated A–Z catalogue.
type CatalogReport struct {
	Letter     string            `json:"letter,omitempty"`
	Type       string            `json:"type,omitempty"`
	Status     string            `json:"status,omitempty"`
	Page       int               `json:"page"`
	TotalPages int               `json:"total_pages"`
	Total      int               `json:"total"`
	Alphabet   []AlphabetCount   `json:"alphabet"`
	Series     []DiscoverySeries `json:"series"`
}

// Genre is one entry of the genre index.
type Genre struct {
	Name  string `json:"name"`
	Slug  string `json:"slug"`
	Count int    `json:"count"`
}

// TrendingReport is the realtime popularity feed.
type TrendingReport struct {
	Period string            `json:"period"`
	Total  int               `json:"total"`
	Series []DiscoverySeries `json:"series"`
}

var (
	articlePattern = regexp.MustCompile(`(?is)<article\b[^>]*>(.*?)</article>`)
	panelPattern   = regexp.MustCompile(`(?is)id="rank-([a-z]+)"`)
	discoveryImg   = regexp.MustCompile(`(?is)<img\b[^>]*>`)
	srcPattern     = regexp.MustCompile(`(?is)\b(?:data-src|src)\s*=\s*["']([^"']+)["']`)
	dataSrcPattern = regexp.MustCompile(`(?is)\bdata-src\s*=\s*["']([^"']+)["']`)
	titleAttr      = regexp.MustCompile(`(?is)\btitle\s*=\s*["']([^"']*)["']`)
	h4LinkPattern  = regexp.MustCompile(`(?is)<h4[^>]*>\s*<a\b[^>]*href\s*=\s*["']([^"']+)["'][^>]*>(.*?)</a>`)
	h3LinkPattern  = regexp.MustCompile(`(?is)<h3[^>]*>\s*(?:<a\b[^>]*>)?(.*?)(?:</a>)?\s*</h3>`)
	classSpan      = func(class string) *regexp.Regexp {
		return regexp.MustCompile(`(?is)<span[^>]*class\s*=\s*["'][^"']*\b` + class + `\b[^"']*["'][^>]*>(.*?)</span>`)
	}
	rankNumPattern = classSpan("rank-num")
	ls4sPattern    = classSpan("ls4s")
	judul2Pattern  = classSpan("judul2")
	boldPattern    = regexp.MustCompile(`(?is)<b>(.*?)</b>`)
	new1Pattern    = regexp.MustCompile(`(?is)<div class="new1">(.*?)</div>`)
	metaPattern    = regexp.MustCompile(`(?is)<p class="meta">(.*?)</p>`)
	countPattern   = regexp.MustCompile(`(?is)<span class="count">([^<]*)</span>`)
	alphabetLink   = regexp.MustCompile(`(?is)<a\b[^>]*href\s*=\s*["'][^"']*huruf=([^"&]+)[^"']*["'][^>]*>(.*?)</a>`)
	genreLink      = regexp.MustCompile(`(?is)<a\b[^>]*class="gnr"[^>]*data-nama="([^"]*)"[^>]*>(.*?)</a>`)
	genreCount     = regexp.MustCompile(`(?is)<i>(.*?)</i>`)
	genreName      = regexp.MustCompile(`(?is)<b>(.*?)</b>`)
	pageInfo       = regexp.MustCompile(`(?is)Halaman\s+(\d+)\s+dari\s+(\d+)`)
	// The catalogue prints "(7,893 komik)" in its page-info block; the
	// letter-filtered variant additionally says "Menampilkan … N judul".
	displayTotal   = regexp.MustCompile(`(?is)\(([\d.,]+)\s+komik\)`)
	thumbHost      = regexp.MustCompile(`(?i)^https?://thumbnail\.komiku\.to/`)
	chapterLink    = regexp.MustCompile(`(?is)<a\b[^>]*href\s*=\s*["']([^"']+)["'][^>]*class="ls24"[^>]*>(.*?)</a>`)
	chapterLinkAlt = regexp.MustCompile(`(?is)<a\b[^>]*class="ls24"[^>]*href\s*=\s*["']([^"']+)["'][^>]*>(.*?)</a>`)
	discoveryHref  = regexp.MustCompile(`(?is)href\s*=\s*["']([^"']+)["']`)
	cardLink       = regexp.MustCompile(`(?is)<h4[^>]*>\s*<a\b[^>]*href\s*=\s*["']([^"']+)["'][^>]*>(.*?)</a>`)
	feedLink       = regexp.MustCompile(`(?is)<a\b[^>]*href\s*=\s*["']([^"']*/manga/[^"']+)["']`)
)

// normalizeStatus folds the three spellings komiku.org uses for a finished
// series into one label, so the UI can compare statuses reliably.
func normalizeStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return ""
	case "end", "ended", "complete", "completed", "tamat", "selesai":
		return "Tamat"
	case "ongoing", "berjalan", "publishing":
		return "Ongoing"
	case "hiatus":
		return "Hiatus"
	}
	return titleCase(value)
}

// titleCase upper-cases the first letter of a lowercase identifier.
func titleCase(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

// normalizeThumbnail points image URLs at the host this network can reach.
// Komiku serves the same paths from both hosts, but the .to host does not
// resolve reliably.
func normalizeThumbnail(raw string) string {
	raw = strings.TrimSpace(html.UnescapeString(raw))
	if raw == "" {
		return ""
	}
	if thumbHost.MatchString(raw) {
		return thumbHost.ReplaceAllString(raw, "https://thumbnail.komiku.org/")
	}
	return raw
}

// extractText strips tags and normalizes whitespace.
func extractText(raw string) string {
	text := tagPattern.ReplaceAllString(raw, " ")
	text = html.UnescapeString(text)
	return strings.Join(strings.Fields(text), " ")
}

// coverFromBlock returns the real cover URL. Komiku lazy-loads covers, so the
// placeholder src must lose to data-src.
func coverFromBlock(block string) string {
	tag := discoveryImg.FindString(block)
	if tag == "" {
		return ""
	}
	if real := firstMatch(dataSrcPattern, tag); real != "" {
		return normalizeThumbnail(real)
	}
	return normalizeThumbnail(firstMatch(srcPattern, tag))
}

func firstMatch(pattern *regexp.Regexp, raw string) string {
	match := pattern.FindStringSubmatch(raw)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

func parseCount(value string) int64 {
	var digits strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	number, _ := strconv.ParseInt(digits.String(), 10, 64)
	return number
}

// seriesSlugFromURL keeps the last path segment of a Komiku series URL.
func seriesSlugFromURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	slug := parts[len(parts)-1]
	if slug == "manga" {
		return ""
	}
	return slug
}

// typeFromCover reads the title type that Komiku embeds in cover filenames
// ("manga_thumbnail-Manhua-Overgeared-New.jpg").
func typeFromCover(cover string) string {
	lower := strings.ToLower(cover)
	for _, kind := range []string{"manhwa", "manhua", "manga"} {
		if strings.Contains(lower, "-"+kind+"-") {
			return titleCase(kind)
		}
	}
	return ""
}

// ParseRankingPeriod normalizes the user-facing ranking period.
func ParseRankingPeriod(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "harian", "daily":
		return "harian", nil
	case "mingguan", "weekly":
		return "mingguan", nil
	case "bulanan", "monthly":
		return "bulanan", nil
	case "total", "all":
		return "total", nil
	}
	return "", fmt.Errorf("invalid ranking period %q: use harian, mingguan, bulanan or total", value)
}

// ParseTrendingPeriod normalizes the trending window.
func ParseTrendingPeriod(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "24h", "24jam", "daily", "harian":
		return "daily", nil
	case "7d", "7hari", "weekly", "mingguan":
		return "weekly", nil
	case "30d", "30hari", "monthly", "bulanan":
		return "monthly", nil
	case "active", "realtime", "live":
		return "active", nil
	}
	return "", fmt.Errorf("invalid trending period %q: use 24h, 7d, 30d or active", value)
}

// Ranking fetches one ranking board from /p/ranking/.
func (c *Client) Ranking(ctx context.Context, period string) ([]DiscoverySeries, error) {
	period, err := ParseRankingPeriod(period)
	if err != nil {
		return nil, err
	}
	base, err := c.base()
	if err != nil {
		return nil, err
	}
	target := base.ResolveReference(&url.URL{Path: "/p/ranking/"})
	data, err := c.fetchHTML(ctx, target.String())
	if err != nil {
		return nil, err
	}
	series, err := ExtractRanking(data, period, target.String())
	if err != nil {
		return nil, err
	}
	// The ranking page carries no type, so fill it from the analytics feed by
	// slug. Best effort: an analytics failure must not break the board.
	if enriched, enrichErr := c.Trending(ctx, "daily", 100); enrichErr == nil {
		bySlug := make(map[string]DiscoverySeries, len(enriched.Series))
		for _, entry := range enriched.Series {
			if entry.Slug != "" {
				bySlug[entry.Slug] = entry
			}
		}
		for index := range series {
			entry, ok := bySlug[series[index].Slug]
			if !ok {
				continue
			}
			if series[index].Type == "" {
				series[index].Type = entry.Type
			}
			if series[index].Status == "" {
				series[index].Status = entry.Status
			}
			if len(series[index].Genres) == 0 {
				series[index].Genres = entry.Genres
			}
		}
	}
	return series, nil
}

// rankPanel returns the markup of one ranking board. Each board is delimited
// by the next board's start marker, so a plain substring slice replaces the
// lookahead a RE2 regexp cannot express.
func rankPanel(body, period string) string {
	starts := panelPattern.FindAllStringSubmatchIndex(body, -1)
	for index, start := range starts {
		if !strings.EqualFold(body[start[2]:start[3]], period) {
			continue
		}
		begin := start[0]
		end := len(body)
		if index+1 < len(starts) {
			end = starts[index+1][0]
		}
		// Trim the trailing closing tags so the slice ends at this board.
		panel := body[begin:end]
		if cut := strings.LastIndex(panel, "</article>"); cut >= 0 {
			panel = panel[:cut+len("</article>")]
		}
		return panel
	}
	return ""
}

// ExtractRanking parses one ranking panel.
func ExtractRanking(data []byte, period, baseURL string) ([]DiscoverySeries, error) {
	body := string(data)
	panel := rankPanel(body, period)
	if panel == "" {
		return nil, fmt.Errorf("ranking page has no %q panel", period)
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	var series []DiscoverySeries
	for _, match := range articlePattern.FindAllStringSubmatch(panel, -1) {
		item := match[1]
		href := ""
		if h := h4LinkPattern.FindStringSubmatch(item); len(h) == 3 {
			href = html.UnescapeString(h[1])
		}
		title := extractText(func() string {
			if h := h4LinkPattern.FindStringSubmatch(item); len(h) == 3 {
				return h[2]
			}
			return ""
		}())
		if title == "" || href == "" {
			continue
		}
		entry := DiscoverySeries{Title: title, Slug: seriesSlugFromURL(href)}
		if base != nil {
			entry.URL = base.ResolveReference(&url.URL{Path: href}).String()
		} else {
			entry.URL = href
		}
		entry.Cover = coverFromBlock(item)
		entry.Type = typeFromCover(entry.Cover)
		entry.Rank = int(parseCount(firstMatch(rankNumPattern, item)))
		if meta := extractText(firstMatch(ls4sPattern, item)); meta != "" {
			parts := strings.Split(meta, "·")
			if len(parts) > 0 {
				for _, genre := range strings.Split(parts[0], ",") {
					if g := strings.TrimSpace(genre); g != "" {
						entry.Genres = append(entry.Genres, g)
					}
				}
			}
			if len(parts) > 1 {
				entry.Views = parseCount(parts[1])
			}
		}
		if chapter := chapterLink.FindStringSubmatch(item); len(chapter) == 3 {
			entry.Chapter = extractText(chapter[2])
			if base != nil {
				entry.ChapterURL = base.ResolveReference(&url.URL{Path: html.UnescapeString(chapter[1])}).String()
			}
		} else if chapter := chapterLinkAlt.FindStringSubmatch(item); len(chapter) == 3 {
			entry.Chapter = extractText(chapter[2])
			if base != nil {
				entry.ChapterURL = base.ResolveReference(&url.URL{Path: html.UnescapeString(chapter[1])}).String()
			}
		}
		series = append(series, entry)
	}
	if len(series) == 0 {
		return nil, fmt.Errorf("ranking panel %q contained no entries", period)
	}
	return series, nil
}

type analyticsItem struct {
	Rank              int             `json:"rank"`
	Title             string          `json:"title"`
	Type              string          `json:"type"`
	Genres            []string        `json:"genres"`
	PublicationStatus string          `json:"publicationStatus"`
	Thumbnail         string          `json:"thumbnail"`
	ActiveUsers       int             `json:"activeUsers"`
	Views             json.RawMessage `json:"views"`
}

// analyticsViews accepts both shapes the API returns: a bare number for the
// daily/weekly/monthly endpoints and an object for the realtime one.
type analyticsViews struct {
	Daily   int64 `json:"daily"`
	Weekly  int64 `json:"weekly"`
	Monthly int64 `json:"monthly"`
	Total   int64 `json:"total"`
	Scalar  int64
}

func (v *analyticsViews) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if trimmed[0] != '{' {
		number, err := strconv.ParseInt(strings.Trim(trimmed, `"`), 10, 64)
		if err != nil {
			return err
		}
		v.Scalar = number
		return nil
	}
	type plain analyticsViews
	return json.Unmarshal(data, (*plain)(v))
}

// forPeriod picks the view count matching the requested window.
func (v analyticsViews) forPeriod(period string) int64 {
	if v.Scalar > 0 {
		return v.Scalar
	}
	switch period {
	case "weekly":
		return v.Weekly
	case "monthly":
		return v.Monthly
	default:
		return v.Daily
	}
}

type analyticsReport struct {
	Status bool            `json:"status"`
	Period string          `json:"period"`
	Total  int             `json:"total"`
	Items  []analyticsItem `json:"items"`
}

// Trending fetches the realtime popularity feed from the analytics API.
func (c *Client) Trending(ctx context.Context, period string, limit int) (TrendingReport, error) {
	period, err := ParseTrendingPeriod(period)
	if err != nil {
		return TrendingReport{}, err
	}
	if limit <= 0 {
		limit = 40
	}
	if limit > 100 {
		limit = 100
	}
	target := fmt.Sprintf("https://analytics.komiku.org/api/popular/%s/?format=json&limit=%d", period, limit)
	data, err := c.fetchHTML(ctx, target)
	if err != nil {
		return TrendingReport{}, err
	}
	return ExtractTrending(data, period)
}

// ExtractTrending parses the analytics JSON payload.
func ExtractTrending(data []byte, period string) (TrendingReport, error) {
	var raw analyticsReport
	if err := json.Unmarshal(data, &raw); err != nil {
		return TrendingReport{}, fmt.Errorf("decode trending payload: %w", err)
	}
	if !raw.Status && len(raw.Items) == 0 {
		return TrendingReport{}, errors.New("trending API returned no items")
	}
	report := TrendingReport{Period: period, Total: raw.Total, Series: make([]DiscoverySeries, 0, len(raw.Items))}
	for _, item := range raw.Items {
		var views analyticsViews
		if len(item.Views) > 0 {
			if err := views.UnmarshalJSON(item.Views); err != nil {
				return TrendingReport{}, fmt.Errorf("decode views for %q: %w", item.Title, err)
			}
		}
		entry := DiscoverySeries{
			Title:  strings.TrimSpace(item.Title),
			Cover:  normalizeThumbnail(item.Thumbnail),
			Type:   titleCase(item.Type),
			Genres: item.Genres,
			Status: titleCase(item.PublicationStatus),
			Rank:   item.Rank,
			Views:  views.forPeriod(period),
		}
		entry.Slug = slugFromCover(entry.Cover)
		if entry.Slug != "" {
			entry.URL = "https://komiku.org/manga/" + entry.Slug + "/"
		}
		report.Series = append(report.Series, entry)
	}
	if len(report.Series) == 0 {
		return TrendingReport{}, errors.New("trending API returned no series")
	}
	return report, nil
}

// base returns the configured Komiku base URL as a parsed URL.
func (c *Client) base() (*url.URL, error) {
	raw := c.BaseURL
	if raw == "" {
		raw = DefaultBaseURL
	}
	base, err := url.Parse(raw)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("invalid base URL %q", raw)
	}
	return base, nil
}

// FeedOptions selects one page of the catalogue feed.
type FeedOptions struct {
	// Order is "update" (latest chapters), "new" (newest titles) or "random".
	Order string
	Type  string
	// Genre and Genre2 combine two genre filters.
	Genre  string
	Genre2 string
	Page   int
}

// NormalizeOrder maps the user-facing feed order onto the site's orderby value.
func NormalizeOrder(order string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(order)) {
	case "", "modified", "latest", "update", "terbaru":
		return "modified", nil
	case "date", "new", "baru":
		return "date", nil
	case "rand", "random", "acak":
		return "rand", nil
	}
	return "", fmt.Errorf("invalid order %q: use update, new or random", order)
}

// LibraryFeed fetches the catalogue feed. orderby=modified returns the latest
// chapters, orderby=date the newest titles.
func (c *Client) LibraryFeed(ctx context.Context, orderby, kind string, page int) ([]DiscoverySeries, error) {
	return c.Browse(ctx, FeedOptions{Order: orderby, Type: kind, Page: page})
}

// Browse fetches one page of the catalogue feed with optional type and genre
// filters.
func (c *Client) Browse(ctx context.Context, options FeedOptions) ([]DiscoverySeries, error) {
	orderby, err := NormalizeOrder(options.Order)
	if err != nil {
		return nil, err
	}
	page := options.Page
	if page < 1 {
		page = 1
	}
	query := url.Values{}
	query.Set("orderby", orderby)
	if options.Type != "" {
		query.Set("tipe", strings.ToLower(strings.TrimSpace(options.Type)))
	}
	if options.Genre != "" {
		query.Set("genre", strings.ToLower(strings.TrimSpace(options.Genre)))
	}
	if options.Genre2 != "" {
		query.Set("genre2", strings.ToLower(strings.TrimSpace(options.Genre2)))
	}
	if page > 1 {
		query.Set("page", strconv.Itoa(page))
	}
	target := "https://api.komiku.org/manga/?" + query.Encode()
	data, err := c.fetchHTML(ctx, target)
	if err != nil {
		return nil, err
	}
	return ExtractFeed(data)
}

// ExtractFeed parses the api.komiku.org/manga/ fragment.
func ExtractFeed(data []byte) ([]DiscoverySeries, error) {
	body := string(data)
	blocks := splitBlocks(body, `class="bge"`)
	var series []DiscoverySeries
	for _, block := range blocks {
		link := feedLink.FindStringSubmatch(block)
		title := extractText(firstMatch(h3LinkPattern, block))
		if len(link) < 2 || title == "" {
			continue
		}
		entry := DiscoverySeries{
			Title: title,
			Slug:  seriesSlugFromURL(link[1]),
			URL:   html.UnescapeString(link[1]),
		}
		entry.Cover = coverFromBlock(block)
		if kind := extractText(firstMatch(boldPattern, block)); kind != "" {
			entry.Type = kind
		}
		if meta := extractText(firstMatch(judul2Pattern, block)); meta != "" {
			// Komiku separates the genre list, the update time and the colour
			// flag with either "•" or "|" depending on the endpoint.
			meta = strings.ReplaceAll(meta, "|", "•")
			parts := strings.Split(meta, "•")
			for _, genre := range strings.Split(parts[0], " x ") {
				g := strings.TrimSpace(genre)
				if g != "" {
					entry.Genres = append(entry.Genres, g)
				}
			}
			if len(parts) > 1 {
				entry.Updated = strings.TrimSpace(parts[1])
			}
			for _, part := range parts {
				if strings.Contains(strings.ToLower(part), "berwarna") {
					entry.Color = true
				}
			}
		}
		chapters := new1Pattern.FindAllStringSubmatch(block, -1)
		if len(chapters) > 0 {
			last := chapters[len(chapters)-1][1]
			entry.Chapter = chapterLabel(extractText(last))
			if href := discoveryHref.FindStringSubmatch(last); len(href) == 2 {
				base, _ := url.Parse("https://komiku.org/")
				entry.ChapterURL = base.ResolveReference(&url.URL{Path: html.UnescapeString(href[1])}).String()
			}
		}
		series = append(series, entry)
	}
	if len(series) == 0 {
		return nil, errors.New("feed contained no series")
	}
	return series, nil
}

// chapterLabel strips the "Awal: " / "Terbaru: " prefixes Komiku puts in the
// feed's chapter chip.
func chapterLabel(value string) string {
	value = strings.TrimSpace(value)
	for _, prefix := range []string{"Awal:", "Terbaru:"} {
		value = strings.TrimSpace(strings.TrimPrefix(value, prefix))
	}
	return value
}

// slugFromCover recovers the series slug Komiku embeds in cover URLs
// (…/uploads/manga/<slug>/manga_thumbnail-….jpg).
func slugFromCover(cover string) string {
	parsed, err := url.Parse(cover)
	if err != nil {
		return ""
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for index, segment := range segments {
		if segment == "manga" && index+1 < len(segments) {
			return segments[index+1]
		}
	}
	return ""
}

// splitBlocks slices an HTML document on a repeated class marker, returning
// the content that follows each marker up to the next one.
func splitBlocks(body, marker string) []string {
	indexes := []int{}
	for offset := 0; ; {
		index := strings.Index(body[offset:], marker)
		if index < 0 {
			break
		}
		indexes = append(indexes, offset+index)
		offset += index + len(marker)
	}
	blocks := make([]string, 0, len(indexes))
	for i, start := range indexes {
		end := len(body)
		if i+1 < len(indexes) {
			end = indexes[i+1]
		}
		blocks = append(blocks, body[start:end])
	}
	return blocks
}

// Catalog fetches one page of the A–Z catalogue.
func (c *Client) Catalog(ctx context.Context, letter, kind, status string, page int) (CatalogReport, error) {
	if page < 1 {
		page = 1
	}
	query := url.Values{}
	if letter != "" && !strings.EqualFold(letter, "all") {
		query.Set("huruf", letter)
	}
	if kind != "" {
		query.Set("tipe", kind)
	}
	if status != "" {
		query.Set("status", status)
	}
	if page > 1 {
		query.Set("halaman", strconv.Itoa(page))
	}
	base, err := c.base()
	if err != nil {
		return CatalogReport{}, err
	}
	target := base.ResolveReference(&url.URL{Path: "/daftar-komik/", RawQuery: query.Encode()})
	data, err := c.fetchHTML(ctx, target.String())
	if err != nil {
		return CatalogReport{}, err
	}
	return ExtractCatalog(data, target.String())
}

// ExtractCatalog parses the daftar-komik page.
func ExtractCatalog(data []byte, baseURL string) (CatalogReport, error) {
	body := string(data)
	report := CatalogReport{Page: 1, Alphabet: []AlphabetCount{}, Series: []DiscoverySeries{}}
	if match := pageInfo.FindStringSubmatch(body); len(match) == 3 {
		report.Page, _ = strconv.Atoi(match[1])
		report.TotalPages, _ = strconv.Atoi(match[2])
	}
	if match := displayTotal.FindStringSubmatch(body); len(match) == 2 {
		report.Total = int(parseCount(match[1]))
	}

	// Only the alphabet navigation carries letter counts; scanning the whole
	// document would also match filter and pagination links.
	if start := strings.Index(body, `<nav class="alphabet-nav"`); start >= 0 {
		end := strings.Index(body[start:], "</nav>")
		if end > 0 {
			nav := body[start : start+end]
			seen := map[string]bool{}
			for _, match := range alphabetLink.FindAllStringSubmatch(nav, -1) {
				letter, err := url.QueryUnescape(match[1])
				if err != nil || seen[letter] {
					continue
				}
				seen[letter] = true
				count := int(parseCount(firstMatch(countPattern, match[2])))
				report.Alphabet = append(report.Alphabet, AlphabetCount{Letter: letter, Count: count})
			}
		}
	}

	base, _ := url.Parse(baseURL)
	for _, block := range splitBlocks(body, `class="manga-card"`) {
		card := cardLink.FindStringSubmatch(block)
		if len(card) < 3 {
			continue
		}
		title := extractText(card[2])
		if title == "" {
			continue
		}
		entry := DiscoverySeries{Title: title, Slug: seriesSlugFromURL(card[1])}
		if base != nil {
			entry.URL = base.ResolveReference(&url.URL{Path: html.UnescapeString(card[1])}).String()
		}
		entry.Cover = coverFromBlock(block)
		if meta := firstMatch(metaPattern, block); meta != "" {
			lines := strings.Split(meta, "<br>")
			if len(lines) > 0 {
				parts := strings.Split(extractText(lines[0]), "•")
				if len(parts) > 0 {
					entry.Type = strings.TrimSpace(parts[0])
				}
				if len(parts) > 1 {
					for _, genre := range strings.Split(parts[1], ",") {
						if g := strings.TrimSpace(genre); g != "" {
							entry.Genres = append(entry.Genres, g)
						}
					}
				}
			}
			if len(lines) > 1 {
				status := extractText(lines[1])
				entry.Status = normalizeStatus(strings.TrimPrefix(status, "Status:"))
			}
		}
		if entry.Type == "" {
			entry.Type = typeFromCover(entry.Cover)
		}
		report.Series = append(report.Series, entry)
	}
	if len(report.Series) == 0 {
		return report, errors.New("catalogue page contained no series")
	}
	return report, nil
}

// Genres fetches the genre index.
func (c *Client) Genres(ctx context.Context) ([]Genre, error) {
	base, err := c.base()
	if err != nil {
		return nil, err
	}
	target := base.ResolveReference(&url.URL{Path: "/p/daftar-genre/"})
	data, err := c.fetchHTML(ctx, target.String())
	if err != nil {
		return nil, err
	}
	return ExtractGenres(data)
}

// ExtractGenres parses the genre index page.
func ExtractGenres(data []byte) ([]Genre, error) {
	body := string(data)
	var genres []Genre
	for _, match := range genreLink.FindAllStringSubmatch(body, -1) {
		slug := strings.TrimSpace(match[1])
		if slug == "" {
			continue
		}
		genres = append(genres, Genre{
			Name:  extractText(firstMatch(genreName, match[2])),
			Slug:  slug,
			Count: int(parseCount(firstMatch(genreCount, match[2]))),
		})
	}
	if len(genres) == 0 {
		return nil, errors.New("genre index contained no genres")
	}
	return genres, nil
}

// GenreSeries fetches the titles for one genre.
func (c *Client) GenreSeries(ctx context.Context, genre, orderby, kind string, page int) ([]DiscoverySeries, error) {
	genre = strings.TrimSpace(strings.ToLower(genre))
	if genre == "" {
		return nil, errors.New("genre name is required")
	}
	if page < 1 {
		page = 1
	}
	query := url.Values{}
	switch orderby {
	case "", "modified", "update", "latest":
		orderby = "modified"
	case "date", "new":
		orderby = "date"
	case "rating", "popular", "meta_value_num":
		orderby = "meta_value_num"
	case "rand", "random":
		orderby = "rand"
	default:
		return nil, fmt.Errorf("invalid sort %q: use update, date, rating or random", orderby)
	}
	query.Set("orderby", orderby)
	if kind != "" {
		query.Set("tipe", kind)
	}
	if page > 1 {
		query.Set("page", strconv.Itoa(page))
	}
	target := fmt.Sprintf("https://api.komiku.org/genre/%s/?%s", url.PathEscape(genre), query.Encode())
	data, err := c.fetchHTML(ctx, target)
	if err != nil {
		return nil, err
	}
	return ExtractFeed(data)
}
