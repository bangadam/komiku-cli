package komiku

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExtractCoverURLPrefersOGImage(t *testing.T) {
	fixture := []byte(`<html><head>
<meta property="og:image" content="https://thumbnail.komiku.to/uploads/manga/dandadan/cover.jpg?w=1200">
<meta name="og:image" content="https://example.invalid/second.jpg">
</head><body><img itemprop="image" src="/fallback.jpg"></body></html>`)
	got, err := ExtractCoverURL(fixture, "https://komiku.org/manga/dandadan/")
	if err != nil {
		t.Fatal(err)
	}
	// thumbnail.komiku.to is dead; the mirror host serves the same file.
	want := "https://thumbnail.komiku.org/uploads/manga/dandadan/cover.jpg?w=1200"
	if got != want {
		t.Fatalf("cover = %q, want %q", got, want)
	}
}

func TestExtractCoverURLFallsBackToItempropAndResolvesRelative(t *testing.T) {
	got, err := ExtractCoverURL([]byte(`<img itemprop="image" src="/uploads/cover.png">`), "https://komiku.org/manga/series/")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://komiku.org/uploads/cover.png" {
		t.Fatalf("cover = %q", got)
	}
}

func TestExtractCoverURLRejectsPagesWithoutCover(t *testing.T) {
	if _, err := ExtractCoverURL([]byte(`<html><body>no image</body></html>`), "https://komiku.org/manga/series/"); err == nil {
		t.Fatal("page without cover was accepted")
	}
	if _, err := ExtractCoverURL([]byte(`<meta property="og:image" content="data:image/png;base64,AAA">`), "https://komiku.org/manga/series/"); err == nil {
		t.Fatal("non-http cover scheme was accepted")
	}
}

func TestDownloadCoverSavesRealBytes(t *testing.T) {
	var seriesURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manga/series/":
			fmt.Fprint(w, `<meta property="og:image" content="/cover.png">`)
		case "/cover.png":
			if r.Header.Get("Referer") != seriesURL {
				t.Errorf("referer = %q, want %q", r.Header.Get("Referer"), seriesURL)
			}
			_, _ = w.Write([]byte("\x89PNG\r\n\x1a\ncover"))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := NewClient(server.Client(), 0)
	client.BaseURL = server.URL

	seriesURL = server.URL + "/manga/series/"
	coverURL, err := client.CoverURL(context.Background(), seriesURL)
	if err != nil {
		t.Fatal(err)
	}
	data, format, err := client.DownloadCover(context.Background(), coverURL, seriesURL)
	if err != nil {
		t.Fatal(err)
	}
	if format != PNG {
		t.Fatalf("format = %q", format)
	}
	if !strings.HasPrefix(string(data), "\x89PNG") {
		t.Fatalf("payload = %q", data)
	}
}

func TestDownloadCoverRejectsNonImagePayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html>not an image</html>")
	}))
	defer server.Close()
	client := NewClient(server.Client(), 0)
	_, _, err := client.DownloadCover(context.Background(), server.URL+"/cover.jpg", server.URL+"/manga/x/")
	if err == nil {
		t.Fatal("HTML payload was accepted as a cover")
	}
}
