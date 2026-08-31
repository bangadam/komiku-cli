package komiku

import (
	"reflect"
	"testing"
)

func TestExtractChaptersPreservesActualHrefAndRawIdentity(t *testing.T) {
	html := []byte(`
<a href="/different-prefix-chapter-271-5/">extra</a>
<a href='/different-prefix-chapter-001/'>padded</a>
<a href="/different-prefix-chapter-01/">short</a>
<a href="/different-prefix-chapter-001/">duplicate identical href</a>
<a href="/not-a-chapter/">ignore</a>`)
	chapters, err := ExtractChapters(html, "https://komiku.org/manga/series-slug/")
	if err != nil {
		t.Fatal(err)
	}
	if len(chapters) != 3 {
		t.Fatalf("got %d chapters: %#v", len(chapters), chapters)
	}
	gotRaw := []string{chapters[0].RawID, chapters[1].RawID, chapters[2].RawID}
	if want := []string{"001", "01", "271-5"}; !reflect.DeepEqual(gotRaw, want) {
		t.Fatalf("raw IDs = %v, want %v", gotRaw, want)
	}
	if chapters[2].Display != "271.5" || chapters[2].URL != "https://komiku.org/different-prefix-chapter-271-5/" {
		t.Fatalf("extra chapter changed: %#v", chapters[2])
	}
}

func TestExtractImageURLsSelectorsAndOrder(t *testing.T) {
	html := []byte(`
<img data-src="https://cdn/wrong-thumb.jpg" src="https://cdn/one image.jpg?q=1" class="other klazy later">
<img class="klazy" src="https://cdn/thumbnail-small.jpg">
<img class="ww" src="https://cdn/fallback.jpg">
<img class="klazy extra" src="https://cdn/two.webp">
`)
	if got, want := ExtractImageURLs(html), []string{"https://cdn/one image.jpg?q=1", "https://cdn/two.webp"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("images = %v, want %v", got, want)
	}
	fallback := []byte(`<img class="a ww b" data-src="https://cdn/wrong.jpg" src="https://cdn/fallback.png">`)
	if got, want := ExtractImageURLs(fallback), []string{"https://cdn/fallback.png"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback = %v, want %v", got, want)
	}
	if got := ExtractImageURLs([]byte(`<img class="other" src="https://cdn/no.jpg">`)); len(got) != 0 {
		t.Fatalf("no-image fixture returned %v", got)
	}
}
