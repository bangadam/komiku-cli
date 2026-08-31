package komiku

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestExtractSeriesResultsPreservesMarkupIdentityOrderAndDeduplicates(t *testing.T) {
	fixture := []byte(`
<a class="result" href="/manga/jujutsu-kaisen-indo/"><h3>Jujutsu Kaisen</h3></a>
<a href='/manga/frieren/' title='Sousou no Frieren'>ignored body</a>
<a href="/manga/jujutsu-kaisen-indo/">duplicate</a>
<a href="/different-prefix-chapter-271-5/">chapter</a>`)
	got, err := ExtractSeriesResults(fixture, "https://komiku.org/?s=manga")
	if err != nil {
		t.Fatal(err)
	}
	want := []Series{
		{Title: "Jujutsu Kaisen", Slug: "jujutsu-kaisen-indo", Href: "/manga/jujutsu-kaisen-indo/", URL: "https://komiku.org/manga/jujutsu-kaisen-indo/"},
		{Title: "Sousou no Frieren", Slug: "frieren", Href: "/manga/frieren/", URL: "https://komiku.org/manga/frieren/"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("results = %#v, want %#v", got, want)
	}
}

func TestExtractSeriesResultsEmptyAndMalformed(t *testing.T) {
	got, err := ExtractSeriesResults([]byte(`<p>No comics found.</p>`), "https://komiku.org/")
	if err != nil || len(got) != 0 {
		t.Fatalf("empty results=%#v err=%v", got, err)
	}
	if _, err := ExtractSeriesResults([]byte(`<a href="/manga/no-title/"><img src="cover.jpg"></a>`), "https://komiku.org/"); err == nil {
		t.Fatal("series candidate without title was accepted")
	}
}

func TestExtractSeriesResultsRejectsCrossHostMangaAnchors(t *testing.T) {
	fixture := []byte(`
<a href="https://ads.invalid/manga/sponsored/" title="Sponsored">ad</a>
<a href="//tracker.invalid/manga/tracked/" title="Tracked">tracker</a>
<a href="/manga/real/" title="Real">real</a>`)
	results, err := ExtractSeriesResults(fixture, "https://komiku.org/?s=real")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Slug != "real" || results[0].URL != "https://komiku.org/manga/real/" {
		t.Fatalf("cross-host result leaked: %+v", results)
	}
	results, err = ExtractSeriesResults([]byte(`<a href="https://ads.invalid/manga/sponsored/" title="Sponsored">ad</a>`), "https://komiku.org/")
	if err != nil || len(results) != 0 {
		t.Fatalf("external-only anchors should be ignored: results=%+v err=%v", results, err)
	}
}

func TestClientSearchEscapesQueryAndUsesParser(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" || r.URL.Query().Get("post_type") != "manga" || r.URL.Query().Get("s") != "frieren & fern" {
			t.Fatalf("request URL = %s", r.URL.String())
		}
		fmt.Fprint(w, `<a href="/manga/frieren/"><span>Frieren &amp; Fern</span></a>`)
	}))
	defer server.Close()
	client := NewClient(server.Client(), 0)
	client.BaseURL = server.URL
	results, err := client.Search(context.Background(), " frieren & fern ")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != "Frieren & Fern" || results[0].URL != server.URL+"/manga/frieren/" {
		t.Fatalf("results = %#v", results)
	}
}

func TestClientSearchFollowsProductionShellAndResolvesCanonicalResult(t *testing.T) {
	requests := make([]string, 0, 2)
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.URL.String())
		var body string
		switch len(requests) {
		case 1:
			body = `<div class="daftar"><span hx-get="https://api.komiku.org/?post_type=manga&amp;s=sakamoto" hx-trigger="revealed" hx-swap="afterend"></span></div>`
		case 2:
			body = `
<div class="bge">
  <div class="bgei"><a href="/manga/sakamoto-days/"><img alt="Sakamoto Days"><div class="tpe1_inf"><b>Manga</b> Komedi</div></a></div>
  <div class="kan">
    <a href="/manga/sakamoto-days/"><h3>Sakamoto Days</h3></a>
    <div class="new1"><a href="/sakamoto-days-chapter-271/" title="Sakamoto Days Chapter 271">Chapter 271</a></div>
  </div>
</div>`
		default:
			return nil, fmt.Errorf("unexpected request %s", request.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})}

	client := NewClient(httpClient, 0)
	results, err := client.Search(context.Background(), "sakamoto")
	if err != nil {
		t.Fatal(err)
	}
	want := []Series{{
		Title: "Sakamoto Days",
		Slug:  "sakamoto-days",
		Href:  "/manga/sakamoto-days/",
		URL:   "https://komiku.org/manga/sakamoto-days/",
	}}
	if !reflect.DeepEqual(results, want) {
		t.Fatalf("results = %#v, want %#v", results, want)
	}
	wantRequests := []string{
		"https://komiku.org/?post_type=manga&s=sakamoto",
		"https://api.komiku.org/?post_type=manga&s=sakamoto",
	}
	if !reflect.DeepEqual(requests, wantRequests) {
		t.Fatalf("requests = %#v, want %#v", requests, wantRequests)
	}
}

func TestClientSearchPreservesEmptyAndMalformedResponses(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantError bool
	}{
		{name: "empty", body: `<div class="no-results"><p>Tidak ada hasil pencarian yang ditemukan.</p></div>`},
		{name: "malformed", body: `<div class="bge"><a href="/manga/no-title/"><img src="cover.jpg"></a></div>`, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hits := 0
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				if r.URL.Path == "/" {
					fmt.Fprintf(w, `<span hx-get="%s/fragment/?post_type=manga&amp;s=missing" hx-trigger="revealed"></span>`, server.URL)
					return
				}
				if r.URL.Path != "/fragment/" {
					t.Errorf("unexpected request %s", r.URL)
				}
				fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			client := NewClient(server.Client(), 0)
			client.BaseURL = server.URL
			results, err := client.Search(context.Background(), "missing")
			if test.wantError {
				if err == nil {
					t.Fatalf("malformed results were silently empty: %#v", results)
				}
			} else if err != nil || len(results) != 0 {
				t.Fatalf("empty results=%#v err=%v", results, err)
			}
			if hits != 2 {
				t.Fatalf("requests = %d, want 2", hits)
			}
		})
	}
}

func TestClientSearchRejectsUntrustedDeferredOrigin(t *testing.T) {
	hits := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		hits++
		body := `<span hx-get="https://attacker.invalid/?post_type=manga&amp;s=sakamoto" hx-trigger="revealed"></span>`
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})}
	client := NewClient(httpClient, 0)
	results, err := client.Search(context.Background(), "sakamoto")
	if err == nil || !strings.Contains(err.Error(), "origin is not allowed") {
		t.Fatalf("results=%#v err=%v", results, err)
	}
	if hits != 1 {
		t.Fatalf("untrusted deferred origin was fetched: requests=%d", hits)
	}
}
