package kiryuu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

// cardMarker starts a result card in every advanced-search fragment.
const cardMarker = `<div class="flex rounded-lg overflow-hidden h-46 group-data-[mode=vertical]:hidden">`

// referenceCard is a trimmed copy of the card in the pinned capture
// /tmp/kiryuu-ref/advanced-search-query.html.
const referenceCard = cardMarker + `
<a href="https://v7.kiryuu.to/manga/black-clover/" class="min-w-[120px] w-23 h-full relative">
  <img width="128" height="201" src="https://v7.kiryuu.to/wp-content/uploads/2021/03/black-clover-797101-R3SVFNes.jpg" class="w-full h-full object-cover wp-post-image" alt="Black Clover" loading="lazy" />
</a>
<div class="flex flex-col justify-between px-4 py-1.5 w-full text-white">
  <div class="flex flex-col justify-between items-start">
    <a href="https://v7.kiryuu.to/manga/black-clover/" class="text-base font-medium text-white line-clamp-1 hover:text-purple-400 transition-colors">Black Clover</a>
    <div class="flex items-center my-2">
      <span class="text-sm text-gray-300 mr-4">Chapter 392</span>
      <span class="bg-accent text-xs px-2 py-0.5 rounded-lg">Ongoing</span>
    </div>
    <div class="flex items-center space-x-2 text-xs font-semibold opacity-90 mb-2">
      <div class="flex items-center">
        <svg xmlns="http://www.w3.org/2000/svg" class="h-4 w-4 text-gray-400 mr-1" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path d="M2.458 12C3.732 7.943 7.523 5 12 5c4.478 0 8.268 2.943 9.542 7-1.274 4.057-5.064 7-9.542 7-4.477 0-8.268-2.943-9.542-7z" /></svg>
        <span>5.6M</span>
      </div>
    </div>
  </div>
  <span class="absolute z-1 px-1 py-[4px] right-[2px] top-[2px] h-8 w-8">
    <img src="https://v7.kiryuu.to/wp-content/themes/kiryuu/static/svg/manga.svg" alt="manga" loading="lazy" />
  </span>
  <div class="flex justify-between mx-2 my-2">
    <div class="flex items-center gap-1 font-normal text-xs"><p class="font-normal text-xs">Ongoing</p></div>
  </div>
  <a class="w-full visited:opacity-50 shadow-md" href="https://v7.kiryuu.to/manga/black-clover/chapter-392.756898/">
    <div><p class="inline-block">Chapter 392</p></div>
    <div class="flex justify-between items-center text-[60%] text-gray-400"><time datetime="2026-05-12T15:35:20+00:00">6 months ago</time></div>
  </a>
</div>`

const minimalCard = cardMarker + `<a href="/manga/frieren/">Frieren</a>`

// newFixtureServerClient starts a loopback server and returns a client whose
// base URL points at it. Sleeps are no-ops so retry paths stay fast.
func newFixtureServerClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := NewClient(server.Client(), 0)
	client.BaseURL = server.URL + "/"
	client.Sleep = func(context.Context, time.Duration) error { return nil }
	return client
}

func TestExtractSeriesResultsPreservesIdentityOrderAndDeduplicates(t *testing.T) {
	fixture := []byte(`
<a href="/manga/black-clover/"><img src="/cover.jpg" alt="Black Clover" /></a>
<a href="/manga/black-clover/" class="text-base">Black Clover</a>
<a href="https://v7.kiryuu.to/manga/frieren/">Frieren</a>
<a href="/manga/black-clover/chapter-392.756898/">Chapter 392</a>
<a href="/manga/frieren/chapter-1.1/">Chapter 1</a>`)
	got, err := ExtractSeriesResults(fixture, "https://v7.kiryuu.to/wp-admin/admin-ajax.php?action=advanced_search")
	if err != nil {
		t.Fatal(err)
	}
	want := []Series{
		{Title: "Black Clover", Slug: "black-clover", Href: "/manga/black-clover/", URL: "https://v7.kiryuu.to/manga/black-clover/"},
		{Title: "Frieren", Slug: "frieren", Href: "https://v7.kiryuu.to/manga/frieren/", URL: "https://v7.kiryuu.to/manga/frieren/"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("results = %#v, want %#v", got, want)
	}
}

func TestExtractSeriesResultsResolvesRelativeAndSkipsUntitled(t *testing.T) {
	fixture := []byte(`
<p>No results found for your search.</p>
<a href="/manga/frieren/">Frieren</a>
<a href="/manga/untitled/"></a>`)
	got, err := ExtractSeriesResults(fixture, "http://127.0.0.1:8080/")
	if err != nil {
		t.Fatal(err)
	}
	want := []Series{{Title: "Frieren", Slug: "frieren", Href: "/manga/frieren/", URL: "http://127.0.0.1:8080/manga/frieren/"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("results = %#v, want %#v", got, want)
	}

	empty, err := ExtractSeriesResults([]byte(`<p>No results found for your search.</p>`), "http://127.0.0.1:8080/")
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty results=%#v err=%v", empty, err)
	}
	if _, err := ExtractSeriesResults(fixture, "not a url"); err == nil {
		t.Fatal("invalid base URL was accepted")
	}
}

func TestSearchPostsAdvancedSearchForm(t *testing.T) {
	var gotForm url.Values
	var gotMethod, gotAction string
	client := newFixtureServerClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAction = r.URL.Query().Get("action")
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		gotForm = r.PostForm
		_, _ = w.Write([]byte(referenceCard + minimalCard))
	})

	results, err := client.Search(context.Background(), "  Frieren  ")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %s, want POST", gotMethod)
	}
	if gotAction != "advanced_search" {
		t.Fatalf("action = %q", gotAction)
	}
	wantForm := map[string]string{
		"page":      "1",
		"orderby":   "popular",
		"query":     "Frieren",
		"inclusion": "OR",
		"exclusion": "OR",
		"genre":     "[]",
		"status":    "[]",
		"type":      "[]",
		"project":   "0",
	}
	for key, want := range wantForm {
		if got := gotForm.Get(key); got != want {
			t.Errorf("form %s = %q, want %q", key, got, want)
		}
	}
	if len(results) != 2 || results[0].Title != "Black Clover" || results[1].Title != "Frieren" {
		t.Fatalf("results = %#v", results)
	}
}

func TestSearchRejectsEmptyQueryWithoutRequest(t *testing.T) {
	hits := 0
	client := newFixtureServerClient(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
	})
	for _, query := range []string{"", "   ", "\t\n"} {
		if _, err := client.Search(context.Background(), query); err == nil || !strings.Contains(err.Error(), "empty") {
			t.Fatalf("query %q: err = %v, want empty-query error", query, err)
		}
	}
	if hits != 0 {
		t.Fatalf("empty queries hit the network %d times", hits)
	}
}

func TestSearchSurfacesServerFailure(t *testing.T) {
	client := newFixtureServerClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	if _, err := client.Search(context.Background(), "frieren"); err == nil {
		t.Fatal("server failure was reported as success")
	}
}
