package komiku

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestValidateSeriesURL(t *testing.T) {
	t.Parallel()
	valid := []string{
		"https://komiku.org/manga/series/",
		"https://komiku.org:443/manga/series/",
		"https://sub.komiku.org/manga/series-name/",
		"http://localhost:8080/manga/fixture/",
		"https://127.0.0.1:8443/manga/fixture/",
		"http://127.12.34.56/manga/fixture/",
		"http://[::1]:8080/manga/fixture/",
	}
	for _, raw := range valid {
		if _, err := ValidateSeriesURL(raw); err != nil {
			t.Errorf("ValidateSeriesURL(%q) unexpected error: %v", raw, err)
		}
	}

	invalid := []string{
		"http://komiku.org/manga/series/",
		"https://komiku.org.evil.test/manga/series/",
		"https://..komiku.org/manga/series/",
		"https://evil.test/manga/series/",
		"https://komiku.org:8443/manga/series/",
		"https://sub.komiku.org:444/manga/series/",
		"https://komiku.org:/manga/series/",
		"ftp://localhost/manga/fixture/",
		"https://komiku.org/series/",
		"https://komiku.org/manga/",
		"https://komiku.org/manga/series",
		"https://komiku.org/manga/series/extra/",
		"https://user@komiku.org/manga/series/",
	}
	for _, raw := range invalid {
		if _, err := ValidateSeriesURL(raw); err == nil {
			t.Errorf("ValidateSeriesURL(%q) accepted invalid URL", raw)
		}
	}
}

func TestDiscoverRejectsInvalidSeriesURLBeforeHTTP(t *testing.T) {
	t.Parallel()
	var hits atomic.Int64
	client := NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		hits.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("<html></html>")),
		}, nil
	})}, 0)

	for _, raw := range []string{
		"https://attacker.invalid/manga/series/",
		"https://komiku.org:8443/manga/series/",
	} {
		if _, err := client.Discover(context.Background(), raw); err == nil {
			t.Errorf("Discover accepted invalid series URL %q", raw)
		}
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("invalid discovery made %d HTTP request(s)", got)
	}
}
