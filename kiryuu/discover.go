package kiryuu

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

// AlphabetCount is one letter tile on the catalogue index. Kiryuu publishes
// no initial-letter index, so Catalog always reports an empty alphabet.
type AlphabetCount struct {
	Letter string `json:"letter"`
	Count  int    `json:"count"`
}

// CatalogReport is one page of the catalogue.
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

// TrendingReport is the recently-updated feed that Kiryuu exposes instead of
// a realtime popularity API.
type TrendingReport struct {
	Period string            `json:"period"`
	Total  int               `json:"total"`
	Series []DiscoverySeries `json:"series"`
}

const (
	// cardBlockMarker starts every advanced-search result card.
	cardBlockMarker = `class="flex rounded-lg overflow-hidden h-46`
	// advancedSearchPageSize is the page size the endpoint returns.
	advancedSearchPageSize = 24
	// trendingMaxPages bounds multi-page fetches for Trending.
	trendingMaxPages = 5
	// trendingMaxLimit caps the number of titles Trending may return.
	trendingMaxLimit = 100
	// genrePageSize is the REST maximum; per_page above 100 is rejected.
	genrePageSize = 100
	// maxGenrePages bounds REST pagination for the genre taxonomy.
	maxGenrePages = 3
)

var (
	discoveryImgPattern  = regexp.MustCompile(`(?is)<img\b[^>]*>`)
	discoverySrcPattern  = regexp.MustCompile(`(?is)\bsrc\s*=\s*["']([^"']+)["']`)
	typeBadgePattern     = regexp.MustCompile(`(?is)/static/svg/([a-z-]+)\.svg`)
	statusBadgePattern   = regexp.MustCompile(`(?is)<span[^>]*class\s*=\s*["'][^"']*\bbg-accent\b[^"']*["'][^>]*>(.*?)</span>`)
	statusTextPattern    = regexp.MustCompile(`(?is)<p[^>]*class\s*=\s*["'][^"']*\bfont-normal\b[^"']*\btext-xs\b[^"']*["'][^>]*>(.*?)</p>`)
	chapterSpanPattern   = regexp.MustCompile(`(?is)<span[^>]*class\s*=\s*["'][^"']*\btext-sm\b[^"']*\btext-gray-300\b[^"']*["'][^>]*>(.*?)</span>`)
	inlineChapterPattern = regexp.MustCompile(`(?is)<p[^>]*class\s*=\s*["'][^"']*\binline-block\b[^"']*["'][^>]*>(.*?)</p>`)
	chapterHrefPattern   = regexp.MustCompile(`(?is)<a\b[^>]*\bhref\s*=\s*["']([^"']*/chapter-[^"']*)["']`)
	timePattern          = regexp.MustCompile(`(?is)<time\b[^>]*>(.*?)</time>`)
	bareSpanPattern      = regexp.MustCompile(`(?is)<span>([^<]*)</span>`)
)

// NormalizeStatus folds the spellings Kiryuu uses for a series state into one
// English label, so callers can compare and display statuses reliably:
// "Completed", "Ongoing", or "Hiatus".
func NormalizeStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return ""
	case "end", "ended", "complete", "completed", "tamat", "selesai":
		return "Completed"
	case "ongoing", "berjalan", "publishing":
		return "Ongoing"
	case "hiatus", "on hiatus", "on-hiatus":
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

// ParseRankingPeriod normalizes the user-facing ranking period. Kiryuu has a
// single all-time popular board, so the period only labels the request.
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
	return "", fmt.Errorf("invalid ranking period %q: use daily, weekly, monthly, or all", value)
}

// ParseTrendingPeriod normalizes the trending window. Kiryuu only publishes a
// recently-updated feed, so the window labels the request.
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

// Ranking fetches the popular board. Kiryuu exposes one all-time board, so
// every accepted period returns the same ordered list.
func (c *Client) Ranking(ctx context.Context, period string) ([]DiscoverySeries, error) {
	if _, err := ParseRankingPeriod(period); err != nil {
		return nil, err
	}
	series, err := c.advancedSearch(ctx, advancedSearchForm(1, "popular"))
	if err != nil {
		return nil, err
	}
	for index := range series {
		series[index].Rank = index + 1
	}
	return series, nil
}

// Trending fetches the recently-updated feed and slices it to limit. Multiple
// pages are fetched when the limit exceeds one page.
func (c *Client) Trending(ctx context.Context, period string, limit int) (TrendingReport, error) {
	period, err := ParseTrendingPeriod(period)
	if err != nil {
		return TrendingReport{}, err
	}
	if limit <= 0 {
		limit = 40
	}
	if limit > trendingMaxLimit {
		limit = trendingMaxLimit
	}
	series := make([]DiscoverySeries, 0, limit)
	seen := make(map[string]bool, limit)
	for page := 1; page <= trendingMaxPages && len(series) < limit; page++ {
		pageSeries, err := c.advancedSearch(ctx, advancedSearchForm(page, "latest"))
		if err != nil {
			if page == 1 {
				return TrendingReport{}, err
			}
			break
		}
		for _, entry := range pageSeries {
			if seen[entry.Slug] {
				continue
			}
			seen[entry.Slug] = true
			series = append(series, entry)
			if len(series) == limit {
				break
			}
		}
		if len(pageSeries) < advancedSearchPageSize {
			break
		}
	}
	if len(series) == 0 {
		return TrendingReport{}, errors.New("trending feed contained no series")
	}
	for index := range series {
		series[index].Rank = index + 1
	}
	return TrendingReport{Period: period, Total: len(series), Series: series}, nil
}

// FeedOptions selects one page of the catalogue feed.
type FeedOptions struct {
	// Order is "update" (latest chapters), "date" (same feed) or "random".
	Order string
	Type  string
	// Genre and Genre2 combine two genre filters.
	Genre  string
	Genre2 string
	Page   int
}

// NormalizeOrder maps the user-facing feed order onto the endpoint's orderby
// value.
func NormalizeOrder(order string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(order)) {
	case "", "latest", "modified", "update", "terbaru", "date", "new", "baru":
		return "latest", nil
	case "rating":
		return "rating", nil
	case "popular":
		return "popular", nil
	case "rand", "random", "acak":
		return "popular", nil
	}
	return "", fmt.Errorf("invalid order %q: use update, date, rating or random", order)
}

// LibraryFeed fetches the recently-updated catalogue feed.
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
	form := advancedSearchForm(options.Page, orderby)
	if options.Type != "" {
		form.Set("type", jsonArray(options.Type))
	}
	if genres := jsonArray(options.Genre, options.Genre2); genres != "[]" {
		form.Set("genre", genres)
	}
	return c.advancedSearch(ctx, form)
}

// Catalog fetches one page of the catalogue. Kiryuu's advanced search has no
// initial-letter filter, so letter is echoed in the report but does not
// narrow the request, and the alphabet index stays empty. The endpoint also
// exposes no catalogue total, so Total counts the titles on this page.
func (c *Client) Catalog(ctx context.Context, letter, kind, status string, page int) (CatalogReport, error) {
	if page < 1 {
		page = 1
	}
	form := advancedSearchForm(page, "popular")
	if kind != "" {
		form.Set("type", jsonArray(kind))
	}
	if slug := statusSlug(status); slug != "" {
		form.Set("status", jsonArray(slug))
	}
	series, err := c.advancedSearch(ctx, form)
	if err != nil {
		return CatalogReport{}, err
	}
	return CatalogReport{
		Letter:   letter,
		Type:     kind,
		Status:   status,
		Page:     page,
		Total:    len(series),
		Alphabet: []AlphabetCount{},
		Series:   series,
	}, nil
}

// statusSlug maps a user-facing status onto the manga-status taxonomy slug.
func statusSlug(value string) string {
	switch NormalizeStatus(value) {
	case "":
		return ""
	case "Completed":
		return "completed"
	case "Ongoing":
		return "ongoing"
	case "Hiatus":
		return "on-hiatus"
	}
	return strings.ToLower(strings.TrimSpace(value))
}

// Genres fetches the full genre taxonomy. The REST endpoint caps per_page at
// 100, so the index is collected page by page.
func (c *Client) Genres(ctx context.Context) ([]Genre, error) {
	genres := make([]Genre, 0, genrePageSize)
	seen := make(map[string]bool, genrePageSize)
	for page := 1; page <= maxGenrePages; page++ {
		target, err := c.apiURL("/wp-json/wp/v2/genre?per_page=" + strconv.Itoa(genrePageSize) +
			"&_fields=name,slug,count&page=" + strconv.Itoa(page))
		if err != nil {
			return nil, err
		}
		data, err := c.fetchHTML(ctx, target)
		if err != nil {
			return nil, err
		}
		pageGenres, err := ExtractGenres(data)
		if err != nil {
			// The first page must parse; a later empty page just ends the walk.
			if page > 1 {
				break
			}
			return nil, err
		}
		for _, genre := range pageGenres {
			if seen[genre.Slug] {
				continue
			}
			seen[genre.Slug] = true
			genres = append(genres, genre)
		}
		if len(pageGenres) < genrePageSize {
			break
		}
	}
	if len(genres) == 0 {
		return nil, errors.New("genre index contained no genres")
	}
	return genres, nil
}

// wpGenre is one taxonomy object from the genres REST endpoint.
type wpGenre struct {
	Name  string `json:"name"`
	Slug  string `json:"slug"`
	Count int    `json:"count"`
}

// ExtractGenres parses one page of the genre taxonomy JSON.
func ExtractGenres(data []byte) ([]Genre, error) {
	var raw []wpGenre
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode genre index: %w", err)
	}
	genres := make([]Genre, 0, len(raw))
	for _, item := range raw {
		slug := strings.TrimSpace(item.Slug)
		if slug == "" {
			continue
		}
		genres = append(genres, Genre{
			Name:  strings.TrimSpace(item.Name),
			Slug:  slug,
			Count: item.Count,
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
	orderby, err := NormalizeOrder(orderby)
	if err != nil {
		return nil, err
	}
	form := advancedSearchForm(page, orderby)
	form.Set("genre", jsonArray(genre))
	if kind != "" {
		form.Set("type", jsonArray(kind))
	}
	return c.advancedSearch(ctx, form)
}

// advancedSearch POSTs one advanced_search form and parses the result cards.
func (c *Client) advancedSearch(ctx context.Context, form url.Values) ([]DiscoverySeries, error) {
	target, err := c.ajaxURL("advanced_search")
	if err != nil {
		return nil, err
	}
	data, status, statusText, err := c.fetchPost(ctx, target, form)
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("POST %s: %s", target, statusText)
	}
	return ExtractCards(data, target)
}

// ExtractCards parses an advanced-search fragment (one card per series).
func ExtractCards(data []byte, baseURL string) ([]DiscoverySeries, error) {
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("invalid discovery base URL %q", baseURL)
	}
	blocks := splitCardBlocks(string(data), cardBlockMarker)
	if len(blocks) == 0 {
		return nil, errors.New("feed contained no series")
	}
	series := make([]DiscoverySeries, 0, len(blocks))
	seen := make(map[string]bool, len(blocks))
	for _, block := range blocks {
		entry, ok := cardSeries(block, base)
		if !ok || seen[entry.Slug] {
			continue
		}
		seen[entry.Slug] = true
		series = append(series, entry)
	}
	if len(series) == 0 {
		return nil, errors.New("feed contained no series")
	}
	return series, nil
}

// cardSeries maps one result card onto DiscoverySeries. Fields the card does
// not expose stay at their zero value.
func cardSeries(block string, base *url.URL) (DiscoverySeries, bool) {
	var anchor seriesAnchor
	found := false
	for _, candidate := range seriesAnchors(block, base) {
		if candidate.Title != "" {
			anchor = candidate
			found = true
			break
		}
	}
	if !found {
		return DiscoverySeries{}, false
	}
	entry := DiscoverySeries{Title: anchor.Title, Slug: anchor.Slug, URL: anchor.URL}
	if img := discoveryImgPattern.FindString(block); img != "" {
		if cover, ok := resolveAgainst(base, cardFirstMatch(discoverySrcPattern, img)); ok {
			entry.Cover = cover
		}
	}
	if kind := cardFirstMatch(typeBadgePattern, block); kind != "" {
		entry.Type = titleCase(kind)
	}
	status := cardText(cardFirstMatch(statusBadgePattern, block))
	if status == "" {
		status = cardText(cardFirstMatch(statusTextPattern, block))
	}
	entry.Status = NormalizeStatus(status)
	chapter := cardText(cardFirstMatch(chapterSpanPattern, block))
	if chapter == "" {
		chapter = cardText(cardFirstMatch(inlineChapterPattern, block))
	}
	entry.Chapter = chapter
	if href := cardFirstMatch(chapterHrefPattern, block); href != "" {
		if resolved, ok := resolveAgainst(base, href); ok {
			entry.ChapterURL = resolved
		}
	}
	entry.Updated = cardText(cardFirstMatch(timePattern, block))
	entry.Views = parseCompactCount(cardText(cardFirstMatch(bareSpanPattern, block)))
	return entry, true
}

// splitCardBlocks slices a fragment on the card marker, returning the content
// that follows each marker up to the next one.
func splitCardBlocks(body, marker string) []string {
	var offsets []int
	for offset := 0; ; {
		index := strings.Index(body[offset:], marker)
		if index < 0 {
			break
		}
		offsets = append(offsets, offset+index)
		offset += index + len(marker)
	}
	blocks := make([]string, 0, len(offsets))
	for i, start := range offsets {
		end := len(body)
		if i+1 < len(offsets) {
			end = offsets[i+1]
		}
		blocks = append(blocks, body[start:end])
	}
	return blocks
}

// resolveAgainst resolves raw against base and accepts only HTTP(S) results.
func resolveAgainst(base *url.URL, raw string) (string, bool) {
	raw = strings.TrimSpace(html.UnescapeString(raw))
	if raw == "" {
		return "", false
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	resolved := base.ResolveReference(ref)
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return "", false
	}
	return resolved.String(), true
}

// parseCompactCount reads display counts such as "855", "5.6M" or "1.2K".
func parseCompactCount(value string) int64 {
	value = strings.TrimSpace(strings.ReplaceAll(value, ",", ""))
	if value == "" {
		return 0
	}
	multiplier := int64(1)
	number := value
	switch value[len(value)-1] {
	case 'K', 'k':
		multiplier = 1_000
		number = value[:len(value)-1]
	case 'M', 'm':
		multiplier = 1_000_000
		number = value[:len(value)-1]
	case 'B', 'b':
		multiplier = 1_000_000_000
		number = value[:len(value)-1]
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(number), 64)
	if err != nil {
		return 0
	}
	return int64(parsed * float64(multiplier))
}
