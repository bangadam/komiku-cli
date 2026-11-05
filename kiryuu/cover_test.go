package kiryuu

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const seriesPageURL = "https://v7.kiryuu.to/manga/black-clover/"

func TestExtractCoverURLPrefersComicSeriesImage(t *testing.T) {
	page := []byte(`
<meta property="og:image" content="https://v7.kiryuu.to/wp-content/uploads/og.jpg" />
<script type="application/ld+json">{"@context":"https://schema.org","@graph":[{"@type":"WebPage","thumbnailUrl":"https://v7.kiryuu.to/wp-content/uploads/2021/03/black-clover-thumb.jpg","image":{"@id":"https://v7.kiryuu.to/manga/black-clover/#primaryimage"}}]}</script>
<script type="application/ld+json">{"@context":"https://schema.org","@type":["Book","ComicSeries"],"name":"Black Clover","image":{"@type":"ImageObject","url":"https://v7.kiryuu.to/wp-content/uploads/2021/03/black-clover-768x1206.jpg"},"creativeWorkStatus":"Ongoing"}</script>`)
	got, err := ExtractCoverURL(page, seriesPageURL)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://v7.kiryuu.to/wp-content/uploads/2021/03/black-clover-768x1206.jpg"; got != want {
		t.Fatalf("cover = %q, want %q", got, want)
	}
}

func TestExtractCoverURLFallsBackToThumbnailThenOGImage(t *testing.T) {
	graph := []byte(`<script type="application/ld+json">{"@graph":[{"@type":"WebPage","thumbnailUrl":"https://v7.kiryuu.to/wp-content/uploads/thumb.jpg"}]}</script>`)
	got, err := ExtractCoverURL(graph, seriesPageURL)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://v7.kiryuu.to/wp-content/uploads/thumb.jpg"; got != want {
		t.Fatalf("thumbnail cover = %q, want %q", got, want)
	}

	og := []byte(`<meta property="og:image" content="/wp-content/uploads/relative.jpg?v=1&amp;w=2" />`)
	got, err = ExtractCoverURL(og, seriesPageURL)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://v7.kiryuu.to/wp-content/uploads/relative.jpg?v=1&w=2"; got != want {
		t.Fatalf("og cover = %q, want %q", got, want)
	}
}

func TestExtractCoverURLSkipsBrokenJSONLDAndRejectsPagesWithoutCover(t *testing.T) {
	page := []byte(`
<script type="application/ld+json">{not json}</script>
<script type="application/ld+json">{"@type":"ComicSeries","image":{"url":"https://v7.kiryuu.to/wp-content/uploads/recovered.jpg"}}</script>`)
	got, err := ExtractCoverURL(page, seriesPageURL)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://v7.kiryuu.to/wp-content/uploads/recovered.jpg"; got != want {
		t.Fatalf("cover = %q, want %q", got, want)
	}

	if _, err := ExtractCoverURL([]byte(`<html><body>no cover</body></html>`), seriesPageURL); err == nil {
		t.Fatal("cover-less page was accepted")
	}
	if _, err := ExtractCoverURL([]byte(`<html></html>`), "not a url"); err == nil {
		t.Fatal("invalid page URL was accepted")
	}
}

func TestCoverURLFetchesSeriesPage(t *testing.T) {
	client := newFixtureServerClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/manga/frieren/" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`<script type="application/ld+json">{"@type":["Book","ComicSeries"],"image":{"@type":"ImageObject","url":"/wp-content/uploads/frieren.jpg"}}</script>`))
	})
	seriesURL := client.BaseURL + "manga/frieren/"
	got, err := client.CoverURL(context.Background(), seriesURL)
	if err != nil {
		t.Fatal(err)
	}
	if want := client.BaseURL + "wp-content/uploads/frieren.jpg"; got != want {
		t.Fatalf("cover = %q, want %q", got, want)
	}
	if _, err := client.CoverURL(context.Background(), "https://example.com/manga/frieren/"); err == nil {
		t.Fatal("foreign series host was accepted")
	}
}

func TestDownloadCoverReturnsBytesAndFormat(t *testing.T) {
	image := []byte("\xff\xd8\xff\xe0cover-bytes")
	var referer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		referer = r.Header.Get("Referer")
		_, _ = w.Write(image)
	}))
	defer server.Close()
	client := NewClient(server.Client(), 0)
	client.Sleep = func(context.Context, time.Duration) error { return nil }
	data, format, err := client.DownloadCover(context.Background(), server.URL+"/cover.jpg", server.URL+"/manga/frieren/")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, image) {
		t.Fatalf("cover bytes = %q, want %q", data, image)
	}
	if format != JPEG {
		t.Fatalf("format = %q, want %q", format, JPEG)
	}
	if referer != "" {
		t.Fatalf("referer = %q, want none", referer)
	}
}

func TestDownloadCoverRejectsNonImagePayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>not an image</html>"))
	}))
	defer server.Close()
	client := NewClient(server.Client(), 0)
	client.Sleep = func(context.Context, time.Duration) error { return nil }
	if _, _, err := client.DownloadCover(context.Background(), server.URL+"/cover.jpg", ""); err == nil || !strings.Contains(err.Error(), "invalid image") {
		t.Fatalf("err = %v, want invalid image payload", err)
	}
}
