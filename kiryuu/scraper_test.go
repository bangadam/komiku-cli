package kiryuu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// The fixtures mirror the live shapes captured from v7.kiryuu.to: the series
// page rows (`<div data-chapter-number=…><a href=…/chapter-N.<postID>/>`), and
// the v1 single chapter object whose content holds single-quoted
// cdn.kiryuu.io images next to reader ads on other hosts.
func contentFixtureChapter(base, path string) string {
	return fmt.Sprintf(`<div data-chapter-number="1" class="flex flex-1 border [.visited-chapter]:bg-accent/40! border-secondary/75 relative rounded-md cursor-pointer">
	<a href="%s/manga/fixture/%s/" class="w-full h-full flex items-center justify-between cursor-pointer ">
		<div class="flex w-full items-center text-left justify-between ">
			<p class="text-sm md:text-lg">Chapter row</p>
		</div>
	</a>
</div>`, base, path)
}

// contentFixturePage is a trimmed Black-Clover-shaped series page: chapters
// arrive newest-first, the header repeats the first chapter link with a stray
// leading space inside href, and reader chrome links must be ignored.
func contentFixturePage(base string) string {
	return `<div class="flex flex-col gap-2">` + strings.Join([]string{
		contentFixtureChapter(base, "chapter-10.105"),
		contentFixtureChapter(base, "chapter-2.102"),
		`<a href=" ` + base + `/manga/fixture/chapter-1.101/">Chapter 1</a>`,
		contentFixtureChapter(base, "chapter-1.101"),
		`<a href="` + base + `/manga/fixture/">Fixture</a>`,
		`<a href="` + base + `/manga/fixture/page/2/">Next</a>`,
		`<a href="` + base + `/manga/other-series/chapter-1.999/">Other series</a>`,
		`<a href="https://example.com/manga/fixture/chapter-1.101/">Mirror</a>`,
		`<a class="external">no href</a>`,
	}, "\n") + `</div>`
}

func contentFixtureChapterContent(base string) string {
	return strings.Join([]string{
		`<img src='https://cdn.kiryuu.io/kuma/fixture/chapter-1/1-60ee9a65a8d90.jpg'>`,
		`<img src="https://cdn.kiryuu.io/kuma/fixture/chapter-1/2-60ee9a67ac2ea.jpg">`,
		`<img src="` + base + `/page-1.jpg">`,
		`<img src="https://blogger.googleusercontent.com/img/b/ad.gif" alt="ad">`,
		`<img src='https://i.88cdn.cloud/ad.gif'>`,
	}, "")
}

type contentFixtureRequest struct {
	path  string
	query url.Values
}

type contentFixtureLog struct {
	mu       sync.Mutex
	requests []contentFixtureRequest
}

func (l *contentFixtureLog) record(r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requests = append(l.requests, contentFixtureRequest{path: r.URL.Path, query: r.URL.Query()})
}

func (l *contentFixtureLog) list() []contentFixtureRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]contentFixtureRequest(nil), l.requests...)
}

// newContentFixtureServer serves the pinned fake-server protocol for one
// fixture series (slug "fixture"): the series page from page, plus the v1
// chapter endpoint. Every other path is a 404.
func newContentFixtureServer(t *testing.T, page func(base string) string) (*httptest.Server, *Client, *contentFixtureLog) {
	t.Helper()
	var server *httptest.Server
	log := &contentFixtureLog{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r)
		query := r.URL.Query()
		switch {
		case r.URL.Path == "/manga/fixture/":
			w.Header().Set("Content-Type", "text/html; charset=UTF-8")
			_, _ = io.WriteString(w, page(server.URL))
		case r.URL.Path == "/wp-json/kiru/v1/chapter":
			id := query.Get("id")
			slug := query.Get("slug")
			switch {
			case slug == "fixture-chapter-1" || id == "101" || id == "291810":
				payload, err := json.Marshal(map[string]any{
					"id":        101,
					"slug":      "fixture-chapter-1",
					"title":     "Fixture Chapter 1",
					"number":    "1",
					"url":       server.URL + "/?chapter=fixture-chapter-1",
					"content":   contentFixtureChapterContent(server.URL),
					"parent_id": 42,
				})
				if err != nil {
					t.Errorf("marshal chapter fixture: %v", err)
					return
				}
				_, _ = w.Write(payload)
			case slug == "ads-only":
				_, _ = io.WriteString(w, `{"id":7,"title":"Ads Only","number":"3","content":"<img src=\"https://blogger.googleusercontent.com/ad.gif\"><img src=\"https://v7.kiryuu.to/wp-content/uploads/2026/05/IDKS.gif\">"}`)
			default:
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"code":"invalid_parameters","message":"invalid parameters","data":{"status":400}}`)
			}
		default:
			http.NotFound(w, r)
		}
	})
	server = httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := NewClient(server.Client(), 0)
	client.BaseURL = server.URL + "/"
	return server, client, log
}

func TestDiscoverParsesSeriesPage(t *testing.T) {
	server, client, log := newContentFixtureServer(t, contentFixturePage)
	seriesURL := server.URL + "/manga/fixture/"

	chapters, err := client.Discover(context.Background(), seriesURL)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	want := []Chapter{
		{RawID: "101", Display: "1", Href: server.URL + "/manga/fixture/chapter-1.101/", URL: server.URL + "/manga/fixture/chapter-1.101/", Number: 1},
		{RawID: "102", Display: "2", Href: server.URL + "/manga/fixture/chapter-2.102/", URL: server.URL + "/manga/fixture/chapter-2.102/", Number: 2},
		{RawID: "105", Display: "10", Href: server.URL + "/manga/fixture/chapter-10.105/", URL: server.URL + "/manga/fixture/chapter-10.105/", Number: 10},
	}
	if !reflect.DeepEqual(chapters, want) {
		t.Fatalf("chapters = %#v, want %#v", chapters, want)
	}

	requests := log.list()
	if len(requests) != 1 {
		t.Fatalf("requests = %v, want only the series page fetch", requests)
	}
	if requests[0].path != "/manga/fixture/" {
		t.Fatalf("requested %q, want the series page", requests[0].path)
	}
}

func TestDiscoverChapterMapping(t *testing.T) {
	tests := []struct {
		name string
		page func(base string) string
		want []Chapter
	}{
		{
			name: "dotted and dashed decimal chapters",
			page: func(base string) string {
				return strings.Join([]string{
					contentFixtureChapter(base, "chapter-246.5.751646"),
					`<a href="` + base + `/manga/fixture/chapter-25-5.291810/">Chapter 25.5</a>`,
					contentFixtureChapter(base, "chapter-1.291820"),
				}, "\n")
			},
			want: []Chapter{
				{RawID: "291820", Display: "1", Number: 1},
				{RawID: "291810", Display: "25.5", Number: 25.5},
				{RawID: "751646", Display: "246.5", Number: 246.5},
			},
		},
		{
			name: "padded numbers normalize and ties sort by raw id",
			page: func(base string) string {
				return strings.Join([]string{
					contentFixtureChapter(base, "chapter-25.0.202"),
					contentFixtureChapter(base, "chapter-025.201"),
				}, "\n")
			},
			want: []Chapter{
				{RawID: "201", Display: "25", Number: 25},
				{RawID: "202", Display: "25", Number: 25},
			},
		},
		{
			name: "relative href resolves against the series page and wins dedupe",
			page: func(base string) string {
				return strings.Join([]string{
					`<a href="chapter-1.101/">relative</a>`,
					contentFixtureChapter(base, "chapter-1.101"),
					contentFixtureChapter(base, "chapter-2.102"),
				}, "\n")
			},
			want: []Chapter{
				{RawID: "101", Display: "1", Href: "chapter-1.101/", Number: 1},
				{RawID: "102", Display: "2", Number: 2},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, client, _ := newContentFixtureServer(t, test.page)
			chapters, err := client.Discover(context.Background(), server.URL+"/manga/fixture/")
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			if len(chapters) != len(test.want) {
				t.Fatalf("chapters = %#v, want %#v", chapters, test.want)
			}
			for i, want := range test.want {
				got := chapters[i]
				if got.RawID != want.RawID || got.Display != want.Display || got.Number != want.Number {
					t.Fatalf("chapter %d = %#v, want raw=%s display=%s number=%v", i, got, want.RawID, want.Display, want.Number)
				}
				if want.Href != "" && got.Href != want.Href {
					t.Fatalf("chapter %d Href = %q, want %q (verbatim)", i, got.Href, want.Href)
				}
				if !strings.HasPrefix(got.URL, "http") || !strings.Contains(got.URL, "/manga/fixture/chapter-") {
					t.Fatalf("chapter %d URL = %q, want the absolute chapter link", i, got.URL)
				}
			}
		})
	}
}

func TestDiscoverNoChapterLinks(t *testing.T) {
	server, client, _ := newContentFixtureServer(t, func(base string) string {
		return `<html><body><img src="https://i.88cdn.cloud/ad.gif"><a href="` + base + `/manga/fixture/">Fixture</a></body></html>`
	})
	_, err := client.Discover(context.Background(), server.URL+"/manga/fixture/")
	if err == nil || !strings.Contains(err.Error(), "no chapters found") {
		t.Fatalf("err = %v, want no chapters found", err)
	}
}

func TestDiscoverSeriesPageErrors(t *testing.T) {
	server, client, _ := newContentFixtureServer(t, contentFixturePage)
	_, err := client.Discover(context.Background(), server.URL+"/manga/missing/")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want the missing series page 404", err)
	}
}

func TestDiscoverRejectsForeignSeriesURL(t *testing.T) {
	client := NewClient(nil, 0)
	for _, raw := range []string{
		"https://example.com/manga/fixture/",
		"http://v7.kiryuu.to/manga/fixture/",
		"https://v7.kiryuu.to/manga/",
	} {
		if _, err := client.Discover(context.Background(), raw); err == nil {
			t.Fatalf("Discover(%q) accepted an invalid series URL", raw)
		}
	}
}

func TestExtractChaptersRejectsInvalidSeriesURL(t *testing.T) {
	markup := []byte(`<a href="/manga/fixture/chapter-1.101/">Chapter 1</a>`)
	if _, err := ExtractChapters(markup, "not a url"); err == nil {
		t.Fatal("ExtractChapters accepted an invalid series URL")
	}
}

func TestChapterImagesFromQueryChapterLink(t *testing.T) {
	server, client, log := newContentFixtureServer(t, contentFixturePage)
	chapterURL := server.URL + "/?chapter=fixture-chapter-1"

	images, err := client.ChapterImages(context.Background(), chapterURL)
	if err != nil {
		t.Fatalf("ChapterImages: %v", err)
	}
	want := []string{
		"https://cdn.kiryuu.io/kuma/fixture/chapter-1/1-60ee9a65a8d90.jpg",
		"https://cdn.kiryuu.io/kuma/fixture/chapter-1/2-60ee9a67ac2ea.jpg",
		server.URL + "/page-1.jpg",
	}
	if !reflect.DeepEqual(images, want) {
		t.Fatalf("images = %v, want %v", images, want)
	}
	requests := log.list()
	if len(requests) != 1 || requests[0].path != "/wp-json/kiru/v1/chapter" || requests[0].query.Get("slug") != "fixture-chapter-1" {
		t.Fatalf("requests = %v, want the slug chapter lookup", requests)
	}
	if requests[0].query.Has("id") {
		t.Fatalf("slug lookup also sent id: %v", requests[0].query)
	}
}

func TestChapterImagesFromMangaPathLink(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		wantID string
	}{
		{name: "plain chapter", path: "/manga/fixture/chapter-1.291810/", wantID: "291810"},
		{name: "decimal chapter", path: "/manga/fixture/chapter-25.5.291810/", wantID: "291810"},
		{name: "missing trailing slash", path: "/manga/fixture/chapter-1.291810", wantID: "291810"},
		{name: "numeric chapter number", path: "/manga/fixture/chapter-101.291810/", wantID: "291810"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, client, log := newContentFixtureServer(t, contentFixturePage)
			images, err := client.ChapterImages(context.Background(), server.URL+test.path)
			if err != nil {
				t.Fatalf("ChapterImages: %v", err)
			}
			if len(images) != 3 {
				t.Fatalf("images = %v, want three pages", images)
			}
			requests := log.list()
			if len(requests) != 1 || requests[0].query.Get("id") != test.wantID {
				t.Fatalf("requests = %v, want id=%s", requests, test.wantID)
			}
			if requests[0].query.Has("slug") {
				t.Fatalf("id lookup also sent slug: %v", requests[0].query)
			}
		})
	}
}

func TestChapterImagesErrors(t *testing.T) {
	server, client, _ := newContentFixtureServer(t, contentFixturePage)
	tests := []struct {
		name    string
		url     string
		wantErr string
	}{
		{name: "unparsable url", url: "://bad", wantErr: "invalid chapter URL"},
		{name: "relative url", url: "/manga/fixture/chapter-1.291810/", wantErr: "invalid chapter URL"},
		{name: "unsupported reader link", url: server.URL + "/manga/fixture/", wantErr: "unsupported chapter URL"},
		{name: "api rejects unknown chapter", url: server.URL + "/?chapter=unknown-chapter", wantErr: "400"},
		{name: "only ads in content", url: server.URL + "/?chapter=ads-only", wantErr: "no pages found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := client.ChapterImages(context.Background(), test.url)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("err = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestExtractImageURLs(t *testing.T) {
	tests := []struct {
		name   string
		markup string
		want   []string
	}{
		{
			name: "keeps cdn pages in document order with both quote styles",
			markup: `<img src='https://cdn.kiryuu.io/kuma/black-clover/chapter-1/1-60ee9a65a8d90.jpg'>` +
				`<img src="https://cdn.kiryuu.io/kuma/black-clover/chapter-1/2-60ee9a67ac2ea.jpg" alt="page 2" />` +
				`<img loading="lazy" src='https://cdn.kiryuu.io/H/series/1/3.jpg'>`,
			want: []string{
				"https://cdn.kiryuu.io/kuma/black-clover/chapter-1/1-60ee9a65a8d90.jpg",
				"https://cdn.kiryuu.io/kuma/black-clover/chapter-1/2-60ee9a67ac2ea.jpg",
				"https://cdn.kiryuu.io/H/series/1/3.jpg",
			},
		},
		{
			name: "drops reader ads on other hosts",
			markup: `<img src="https://blogger.googleusercontent.com/img/b/R29vZ2xl/ad.gif" alt="ad">` +
				`<img src="https://v7.kiryuu.to/wp-content/uploads/2026/05/IDKS.gif" alt="">` +
				`<img src="https://i.88cdn.cloud/AOZfj5wvf4FR.gif" alt="">` +
				`<img src="https://secure.gravatar.com/avatar/?s=96&amp;d=mm">` +
				`<img src='https://cdn.kiryuu.io/kuma/fixture/chapter-1/1-aa.jpg'>`,
			want: []string{"https://cdn.kiryuu.io/kuma/fixture/chapter-1/1-aa.jpg"},
		},
		{
			// Trimmed from the live v2 content plus the reader chrome ads.
			name: "real capture keeps consecutive pages and drops chrome ads",
			markup: `<img width="1600" height="500" src="https://v7.kiryuu.to/wp-content/uploads/2021/05/logo-kiryuu-219671-d1yvN4qK.png" class="custom-logo" alt="Kiryuu ID">` +
				`<img src="https://blogger.googleusercontent.com/img/b/R29vZ2xl/AVvXsEhd4XX/ad.gif" alt="Hokidewa">` +
				`<img src="https://cdn.kiryuu.io/ycdn/imgsc/b/black-clover/390/1.jpg" alt="Black Clover Chapter 390 - Page 1" />` +
				`<img src="https://cdn.kiryuu.io/ycdn/imgsc/b/black-clover/390/2.jpg" alt="Black Clover Chapter 390 - Page 2" />` +
				`<img src="https://i.88cdn.cloud/AOZfj5wvf4FR.gif" alt="">` +
				`<img src="https://cdn.kiryuu.io/ycdn/imgsc/b/black-clover/390/3.jpg" alt="Black Clover Chapter 390 - Page 3" />` +
				`<img alt="" src="https://secure.gravatar.com/avatar/?s=96&amp;d=mm&amp;r=g" class="avatar avatar-96 photo" height="96" width="96" />`,
			want: []string{
				"https://cdn.kiryuu.io/ycdn/imgsc/b/black-clover/390/1.jpg",
				"https://cdn.kiryuu.io/ycdn/imgsc/b/black-clover/390/2.jpg",
				"https://cdn.kiryuu.io/ycdn/imgsc/b/black-clover/390/3.jpg",
			},
		},
		{
			name:   "requires https on the image cdn",
			markup: `<img src="http://cdn.kiryuu.io/kuma/fixture/chapter-1/1-aa.jpg">`,
		},
		{
			name:   "ignores data-src and srcset",
			markup: `<img data-src="https://cdn.kiryuu.io/lazy.jpg" srcset="https://cdn.kiryuu.io/set.jpg 1x">`,
		},
		{
			name:   "unescapes html entities",
			markup: `<img src="https://cdn.kiryuu.io/page.jpg?w=1&amp;h=2">`,
			want:   []string{"https://cdn.kiryuu.io/page.jpg?w=1&h=2"},
		},
		{
			name:   "keeps loopback fixture hosts",
			markup: `<img src='http://127.0.0.1:8080/page.jpg'>`,
			want:   []string{"http://127.0.0.1:8080/page.jpg"},
		},
		{
			name:   "no images",
			markup: `<div>no pages here</div>`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ExtractImageURLs([]byte(test.markup))
			if test.want == nil {
				if len(got) != 0 {
					t.Fatalf("images = %v, want none", got)
				}
				return
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("images = %v, want %v", got, test.want)
			}
		})
	}
}
