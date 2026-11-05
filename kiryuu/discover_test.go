package kiryuu

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

const cardBase = "https://v7.kiryuu.to/wp-admin/admin-ajax.php?action=advanced_search"

func TestParseRankingAndTrendingPeriods(t *testing.T) {
	ranking := []struct {
		value string
		want  string
		ok    bool
	}{
		{"", "harian", true},
		{"daily", "harian", true},
		{"harian", "harian", true},
		{"WEEKLY", "mingguan", true},
		{"monthly", "bulanan", true},
		{"all", "total", true},
		{"yearly", "", false},
	}
	for _, test := range ranking {
		got, err := ParseRankingPeriod(test.value)
		if (err == nil) != test.ok || got != test.want {
			t.Fatalf("ParseRankingPeriod(%q) = %q, %v; want %q ok=%v", test.value, got, err, test.want, test.ok)
		}
	}
	trending := []struct {
		value string
		want  string
		ok    bool
	}{
		{"", "daily", true},
		{"24h", "daily", true},
		{"7d", "weekly", true},
		{"30hari", "monthly", true},
		{"realtime", "active", true},
		{"forever", "", false},
	}
	for _, test := range trending {
		got, err := ParseTrendingPeriod(test.value)
		if (err == nil) != test.ok || got != test.want {
			t.Fatalf("ParseTrendingPeriod(%q) = %q, %v; want %q ok=%v", test.value, got, err, test.want, test.ok)
		}
	}
}

func TestNormalizeStatusFoldsKiryuuSpellings(t *testing.T) {
	tests := map[string]string{
		"":          "",
		"Ongoing":   "Ongoing",
		"ongoing":   "Ongoing",
		"Completed": "Completed",
		"tamat":     "Completed",
		"On Hiatus": "Hiatus",
		"on-hiatus": "Hiatus",
		"hiatus":    "Hiatus",
	}
	for value, want := range tests {
		if got := NormalizeStatus(value); got != want {
			t.Fatalf("NormalizeStatus(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestNormalizeOrderMapsFeedOrders(t *testing.T) {
	tests := []struct {
		value string
		want  string
		ok    bool
	}{
		{"", "latest", true},
		{"update", "latest", true},
		{"date", "latest", true},
		{"latest", "latest", true},
		{"rating", "rating", true},
		{"random", "popular", true},
		{"rand", "popular", true},
		{"alphabetical", "", false},
	}
	for _, test := range tests {
		got, err := NormalizeOrder(test.value)
		if (err == nil) != test.ok || got != test.want {
			t.Fatalf("NormalizeOrder(%q) = %q, %v; want %q ok=%v", test.value, got, err, test.want, test.ok)
		}
	}
}

func TestExtractCardsMapsReferenceCardFields(t *testing.T) {
	got, err := ExtractCards([]byte(referenceCard+minimalCard), cardBase)
	if err != nil {
		t.Fatal(err)
	}
	want := []DiscoverySeries{
		{
			Title:      "Black Clover",
			Slug:       "black-clover",
			URL:        "https://v7.kiryuu.to/manga/black-clover/",
			Cover:      "https://v7.kiryuu.to/wp-content/uploads/2021/03/black-clover-797101-R3SVFNes.jpg",
			Type:       "Manga",
			Status:     "Ongoing",
			Views:      5_600_000,
			Chapter:    "Chapter 392",
			ChapterURL: "https://v7.kiryuu.to/manga/black-clover/chapter-392.756898/",
			Updated:    "6 months ago",
		},
		{
			Title: "Frieren",
			Slug:  "frieren",
			URL:   "https://v7.kiryuu.to/manga/frieren/",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cards = %#v\nwant %#v", got, want)
	}
}

func TestExtractCardsNormalizesHiatusAndRejectsEmptyFragments(t *testing.T) {
	hiatus := strings.ReplaceAll(referenceCard, "Ongoing", "On Hiatus")
	got, err := ExtractCards([]byte(hiatus), cardBase)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Status != "Hiatus" {
		t.Fatalf("cards = %#v, want one Hiatus entry", got)
	}
	if _, err := ExtractCards([]byte(`<div class="col-span-full">No results found</div>`), cardBase); err == nil {
		t.Fatal("empty fragment was accepted")
	}
	if _, err := ExtractCards([]byte(referenceCard), "not a url"); err == nil {
		t.Fatal("invalid base URL was accepted")
	}
}

func TestBrowseEncodesArrayFilters(t *testing.T) {
	var got url.Values
	client := newFixtureServerClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Query().Get("action") != "advanced_search" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		got = r.PostForm
		_, _ = w.Write([]byte(referenceCard))
	})
	if _, err := client.Browse(context.Background(), FeedOptions{
		Order:  "update",
		Type:   "Manhwa",
		Genre:  "Action",
		Genre2: "comedy",
		Page:   2,
	}); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"orderby": "latest",
		"page":    "2",
		"type":    `["manhwa"]`,
		"genre":   `["action","comedy"]`,
		"query":   "",
		"status":  "[]",
	}
	for key, value := range want {
		if got.Get(key) != value {
			t.Errorf("form %s = %q, want %q", key, got.Get(key), value)
		}
	}
}

func TestRankingAssignsPositions(t *testing.T) {
	var orderby string
	client := newFixtureServerClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		orderby = r.PostForm.Get("orderby")
		_, _ = w.Write([]byte(referenceCard + minimalCard))
	})
	series, err := client.Ranking(context.Background(), "weekly")
	if err != nil {
		t.Fatal(err)
	}
	if orderby != "popular" {
		t.Fatalf("orderby = %q, want popular", orderby)
	}
	if len(series) != 2 || series[0].Rank != 1 || series[1].Rank != 2 {
		t.Fatalf("ranking = %#v", series)
	}
	if _, err := client.Ranking(context.Background(), "yearly"); err == nil {
		t.Fatal("invalid ranking period was accepted")
	}
}

func TestTrendingSlicesUpdatedFeedToLimit(t *testing.T) {
	client := newFixtureServerClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if got := r.PostForm.Get("orderby"); got != "latest" {
			t.Errorf("orderby = %q, want latest", got)
		}
		_, _ = w.Write([]byte(referenceCard + minimalCard))
	})
	report, err := client.Trending(context.Background(), "24h", 1)
	if err != nil {
		t.Fatal(err)
	}
	if report.Period != "daily" || report.Total != 1 || len(report.Series) != 1 {
		t.Fatalf("report = %#v", report)
	}
	if report.Series[0].Rank != 1 || report.Series[0].Title != "Black Clover" {
		t.Fatalf("series = %#v", report.Series)
	}
}

func TestTrendingPaginatesUntilLimit(t *testing.T) {
	pages := 0
	client := newFixtureServerClient(t, func(w http.ResponseWriter, r *http.Request) {
		pages++
		_ = r.ParseForm()
		switch r.PostForm.Get("page") {
		case "1":
			var body strings.Builder
			for i := range advancedSearchPageSize {
				fmt.Fprintf(&body, `%s<a href="/manga/series-%d/">Series %d</a>`, cardMarker, i, i)
			}
			_, _ = w.Write([]byte(body.String()))
		case "2":
			_, _ = w.Write([]byte(minimalCard))
		default:
			_, _ = w.Write([]byte(`<div class="col-span-full">No results</div>`))
		}
	})
	report, err := client.Trending(context.Background(), "7d", 40)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Series) != advancedSearchPageSize+1 || pages != 2 {
		t.Fatalf("series = %d, pages = %d; want %d and 2", len(report.Series), pages, advancedSearchPageSize+1)
	}
	if report.Series[advancedSearchPageSize].Title != "Frieren" {
		t.Fatalf("second page entry = %#v", report.Series[advancedSearchPageSize])
	}
}

func TestCatalogSendsFiltersAndIgnoresLetter(t *testing.T) {
	var got url.Values
	client := newFixtureServerClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		got = r.PostForm
		_, _ = w.Write([]byte(strings.ReplaceAll(referenceCard, "Ongoing", "On Hiatus")))
	})
	report, err := client.Catalog(context.Background(), "B", "manhwa", "hiatus", 3)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"page":    "3",
		"orderby": "popular",
		"type":    `["manhwa"]`,
		"status":  `["on-hiatus"]`,
	}
	for key, value := range want {
		if got.Get(key) != value {
			t.Errorf("form %s = %q, want %q", key, got.Get(key), value)
		}
	}
	if report.Letter != "B" || report.Type != "manhwa" || report.Status != "hiatus" {
		t.Fatalf("report echo = %#v", report)
	}
	if report.Page != 3 || report.Total != 1 || len(report.Series) != 1 {
		t.Fatalf("report = %#v", report)
	}
	if report.Alphabet == nil || len(report.Alphabet) != 0 {
		t.Fatalf("alphabet = %#v, want empty non-nil", report.Alphabet)
	}
	if report.Series[0].Status != "Hiatus" {
		t.Fatalf("card status = %q", report.Series[0].Status)
	}
}

func TestGenreSeriesSendsGenreAndSort(t *testing.T) {
	var got url.Values
	client := newFixtureServerClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.PostForm
		_, _ = w.Write([]byte(referenceCard))
	})
	if _, err := client.GenreSeries(context.Background(), "Action", "rating", "manga", 2); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"genre": `["action"]`, "orderby": "rating", "type": `["manga"]`, "page": "2"}
	for key, value := range want {
		if got.Get(key) != value {
			t.Errorf("form %s = %q, want %q", key, got.Get(key), value)
		}
	}
	if _, err := client.GenreSeries(context.Background(), " ", "update", "", 1); err == nil {
		t.Fatal("blank genre was accepted")
	}
	if _, err := client.GenreSeries(context.Background(), "action", "alphabetical", "", 1); err == nil {
		t.Fatal("invalid sort was accepted")
	}
}

func TestExtractGenresParsesTaxonomyPage(t *testing.T) {
	got, err := ExtractGenres([]byte(`[{"count":4680,"name":"Action","slug":"action"},{"count":13,"name":"","slug":""}]`))
	if err != nil {
		t.Fatal(err)
	}
	want := []Genre{{Name: "Action", Slug: "action", Count: 4680}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("genres = %#v, want %#v", got, want)
	}
	if _, err := ExtractGenres([]byte(`{"code":"rest_invalid_param"}`)); err == nil {
		t.Fatal("object payload was accepted as a genre list")
	}
	if _, err := ExtractGenres([]byte(`[]`)); err == nil {
		t.Fatal("empty genre list was accepted")
	}
}

func TestGenresWalksTaxonomyPages(t *testing.T) {
	var requested []string
	client := newFixtureServerClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wp-json/wp/v2/genre" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("per_page"); got != "100" {
			t.Errorf("per_page = %q, want 100", got)
		}
		if got := r.URL.Query().Get("_fields"); got != "name,slug,count" {
			t.Errorf("_fields = %q", got)
		}
		page := r.URL.Query().Get("page")
		requested = append(requested, page)
		count := 100
		if page == "2" {
			count = 1
		}
		entries := make([]string, 0, count)
		offset := 0
		if page == "2" {
			offset = 100
		}
		for i := range count {
			entries = append(entries, fmt.Sprintf(`{"name":"Genre %d","slug":"genre-%d","count":%d}`, offset+i, offset+i, offset+i))
		}
		_, _ = w.Write([]byte("[" + strings.Join(entries, ",") + "]"))
	})
	genres, err := client.Genres(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(genres) != 101 {
		t.Fatalf("genres = %d, want 101", len(genres))
	}
	if !reflect.DeepEqual(requested, []string{"1", "2"}) {
		t.Fatalf("pages = %#v", requested)
	}
	if genres[0].Slug != "genre-0" || genres[100].Count != 100 {
		t.Fatalf("genres = %#v", genres[:1])
	}
}
