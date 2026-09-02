package komiku

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestParseVolumeMappingDerivesEnds(t *testing.T) {
	var fixture strings.Builder
	starts := []int{1, 8, 17, 26, 35, 44, 53, 62, 71, 80, 89, 98, 107, 116, 125, 134, 143, 153}
	for i, start := range starts {
		fixture.WriteString(`{"VolumeNumber":{"wt":"`)
		fixture.WriteString(string(rune('1' + i)))
		fixture.WriteString(`"},"link":"x?start=`)
		fixture.WriteString(intString(start))
		fixture.WriteString(`"}`)
	}
	// Use a clearer fixture for the two required assertions plus intermediate starts.
	fixture.Reset()
	for volume, start := range starts {
		fixture.WriteString(`VolumeNumber":{"wt":"`)
		fixture.WriteString(intString(volume + 1))
		fixture.WriteString(`"}, "url":"?start=`)
		fixture.WriteString(intString(start))
		fixture.WriteString(`"} `)
	}
	volumes, err := ParseVolumeMapping([]byte(fixture.String()), 271)
	if err != nil {
		t.Fatal(err)
	}
	if volumes[0] != (Volume{Volume: 1, Start: 1, End: 7}) {
		t.Fatalf("volume 1 = %#v", volumes[0])
	}
	if volumes[16] != (Volume{Volume: 17, Start: 143, End: 152}) {
		t.Fatalf("volume 17 = %#v", volumes[16])
	}
	if volumes[len(volumes)-1].End != 271 {
		t.Fatalf("last volume did not absorb discovered remainder: %#v", volumes[len(volumes)-1])
	}
}

func TestParseVolumeMappingIgnoresDataOutsideVolumesSection(t *testing.T) {
	starts := []int{1, 8, 17, 26, 35, 44, 53, 62, 71, 80, 89, 98, 107, 116, 125, 134, 143, 153}
	var fixture strings.Builder
	fixture.WriteString(`<h2 id="Metadata">Metadata</h2>VolumeNumber":{"wt":"99"},"url":"?start=999"}`)
	fixture.WriteString(`<h2 id="Volumes">Volumes</h2>`)
	for volume, start := range starts {
		fixture.WriteString(`VolumeNumber":{"wt":"`)
		fixture.WriteString(intString(volume + 1))
		fixture.WriteString(`"},"url":"?start=`)
		fixture.WriteString(intString(start))
		fixture.WriteString(`"} `)
	}
	fixture.WriteString(`<h2 id="References">References</h2>VolumeNumber":{"wt":"100"},"url":"?start=1000"}`)

	volumes, err := ParseVolumeMapping([]byte(fixture.String()), 271)
	if err != nil {
		t.Fatal(err)
	}
	if len(volumes) != len(starts) {
		t.Fatalf("volume count = %d, want %d; mapping=%#v", len(volumes), len(starts), volumes)
	}
	if volumes[0] != (Volume{Volume: 1, Start: 1, End: 7}) {
		t.Fatalf("volume 1 = %#v", volumes[0])
	}
	if volumes[16] != (Volume{Volume: 17, Start: 143, End: 152}) {
		t.Fatalf("volume 17 = %#v", volumes[16])
	}
	if volumes[len(volumes)-1] != (Volume{Volume: 18, Start: 153, End: 271}) {
		t.Fatalf("last volume = %#v", volumes[len(volumes)-1])
	}
}

func TestValidateVolumeOverrides(t *testing.T) {
	tests := []struct {
		name    string
		volumes []Volume
		want    string
	}{
		{"duplicate", []Volume{{1, 1, 7}, {1, 8, 12}}, "duplicate volume"},
		{"reversed", []Volume{{1, 8, 7}}, "reversed range"},
		{"overlap", []Volume{{1, 1, 7}, {2, 7, 12}}, "overlaps"},
		{"invalid number", []Volume{{0, 1, 7}}, "non-positive"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateVolumes(test.volumes, 20)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want substring %q", err, test.want)
			}
		})
	}
	if err := ValidateVolumes([]Volume{{1, 1, 7}, {2, 8, 20}}, 20); err != nil {
		t.Fatalf("valid override rejected: %v", err)
	}
}
func TestParseWikipediaDisplayVolumesUsesExactRenderedRows(t *testing.T) {
	fixture := []byte(`
<section aria-labelledby="Volumes">
  <h3 id="Volumes">Volumes</h3>
  <table class="wikitable">
    <tr><th scope="row" id="vol1">1</th><td><table><tr><td><ul>
      <li><b>Days 1:</b> The Legendary Hit Man</li>
      <li><b>Days 2.</b> Sakamoto's Family</li>
      <li><b>Days 3:</b> Sugar Park</li>
    </ul></td></tr></table></td></tr>
    <tr><th scope="row" id="vol2">2</th><td><ul>
      <li>Days 4: The Next Day</li><li>Days 5. Another Day</li>
    </ul></td></tr>
  </table>
</section>
<section aria-labelledby="Sakamoto_Holidays">
  <table class="wikitable"><tr><th scope="row" id="vol99">99</th><td><li>Days 99: Wrong series</li></td></tr></table>
</section>`)

	got, err := ParseWikipediaDisplayVolumes(fixture)
	if err != nil {
		t.Fatal(err)
	}
	want := []Volume{{Volume: 1, Start: 1, End: 3}, {Volume: 2, Start: 4, End: 5}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("volumes = %#v, want %#v", got, want)
	}
	if _, mapped := VolumeForChapter(6, got); mapped {
		t.Fatal("chapter after the final exact Wikipedia row was incorrectly mapped")
	}
}

func TestParseWikipediaDisplayVolumesPreservesSakamotoPublishedBoundary(t *testing.T) {
	want := []Volume{
		{Volume: 1, Start: 1, End: 7},
		{Volume: 2, Start: 8, End: 16},
		{Volume: 17, Start: 143, End: 151},
		{Volume: 18, Start: 152, End: 161},
		{Volume: 28, Start: 246, End: 255},
	}
	var fixture strings.Builder
	fixture.WriteString(`<section aria-labelledby="Volumes"><table class="wikitable">`)
	for _, volume := range want {
		fixture.WriteString(`<tr><th scope="row" id="vol`)
		fixture.WriteString(intString(volume.Volume))
		fixture.WriteString(`">`)
		fixture.WriteString(intString(volume.Volume))
		fixture.WriteString(`</th><td><ul>`)
		for chapter := volume.Start; chapter <= volume.End; chapter++ {
			fixture.WriteString(`<li>Days `)
			fixture.WriteString(intString(chapter))
			fixture.WriteString(`: chapter</li>`)
		}
		fixture.WriteString(`</ul></td></tr>`)
	}
	fixture.WriteString(`</table></section>`)

	got, err := ParseWikipediaDisplayVolumes([]byte(fixture.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("volumes = %#v, want %#v", got, want)
	}
	if _, mapped := VolumeForChapter(256, got); mapped {
		t.Fatal("unpublished Sakamoto chapter 256 was absorbed into volume 28")
	}
}

func TestParseWikipediaDisplayVolumesRejectsIncompleteRows(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"missing chapter list", `<section aria-labelledby="Volumes"><th scope="row" id="vol1">1</th></section>`},
		{"noncontiguous chapter list", `<section aria-labelledby="Volumes"><th scope="row" id="vol1">1</th><li>Days 1: one</li><li>Days 3: three</li></section>`},
		{"duplicate volume", `<section aria-labelledby="Volumes"><th scope="row" id="vol1">1</th><li>Days 1: one</li><th scope="row" id="vol1">1</th><li>Days 2: two</li></section>`},
		{"overlap", `<section aria-labelledby="Volumes"><th scope="row" id="vol1">1</th><li>Days 1: one</li><li>Days 2: two</li><th scope="row" id="vol2">2</th><li>Days 2: two</li></section>`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseWikipediaDisplayVolumes([]byte(test.body)); err == nil {
				t.Fatal("invalid Wikipedia rows were accepted")
			}
		})
	}
}

func TestFetchWikipediaDisplayVolumesMakesOneWikipediaRequest(t *testing.T) {
	var requests []string
	client := NewClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.URL.String())
		body := `<section aria-labelledby="Volumes"><th scope="row" id="vol1">1</th><li>Days 1: one</li></section>`
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}, 0)

	got, err := client.FetchWikipediaDisplayVolumes(context.Background(), "Sakamoto Days")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (Volume{Volume: 1, Start: 1, End: 1}) {
		t.Fatalf("volumes = %#v", got)
	}
	if want := []string{"https://en.wikipedia.org/wiki/List_of_Sakamoto_Days_chapters"}; !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests = %#v, want %#v", requests, want)
	}
}

// A page that exists but has no usable volume grouping ends the lookup without
// reporting an error; every request stays on English Wikipedia (no fandom or
// other-source fallback), and an invalid section still fails loudly.
func TestFetchWikipediaDisplayVolumesDoesNotFallBack(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"no mapping", `<section aria-labelledby="Volumes"><p>No usable rows</p></section>`, false},
		{"invalid mapping", `<section aria-labelledby="Volumes"><th scope="row" id="vol1">1</th></section>`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requests []string
			client := NewClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				requests = append(requests, request.URL.String())
				body := test.body
				if strings.Contains(request.URL.Path, "/w/api.php") {
					body = `{"query":{"search":[]}}`
				}
				return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})}, 0)

			volumes, err := client.FetchWikipediaDisplayVolumes(context.Background(), "Missing Series")
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
			if !test.wantErr && len(volumes) != 0 {
				t.Fatalf("no-map volumes = %#v", volumes)
			}
			if test.wantErr {
				return
			}
			// "no mapping" resolves through the search API and finds nothing:
			// at most one page fetch per candidate plus search queries.
			if len(requests) > 5 {
				t.Fatalf("display lookup made %d requests, want bounded resolution", len(requests))
			}
			for _, target := range requests {
				if !strings.Contains(target, "en.wikipedia.org") {
					t.Fatalf("display lookup left English Wikipedia: %s", target)
				}
			}
		})
	}
}

func TestFetchWikipediaDisplayVolumesResolvesTitleViaSearch(t *testing.T) {
	var requests []string
	chaptersBody := `<section aria-labelledby="Volumes"><th scope="row" id="vol1">1</th><li>Days 1: one</li></section>`
	client := NewClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.URL.String())
		if strings.Contains(request.URL.Path, "/w/api.php") {
			body := `{"query":{"search":[{"title":"List of Noisy Series episodes"},{"title":"List of Noisy Series chapters"}]}}`
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
		}
		if strings.Contains(request.URL.Path, "_Indo_") {
			return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(chaptersBody)), Request: request}, nil
	})}, 0)

	volumes, err := client.FetchWikipediaDisplayVolumes(context.Background(), "Noisy Series Indo")
	if err != nil {
		t.Fatal(err)
	}
	if len(volumes) != 1 || volumes[0] != (Volume{Volume: 1, Start: 1, End: 1}) {
		t.Fatalf("volumes = %#v", volumes)
	}
	resolved := false
	for _, target := range requests {
		if strings.Contains(target, "List_of_Noisy_Series_chapters") {
			resolved = true
		}
	}
	if !resolved {
		t.Fatalf("search resolution did not fetch the chapters page: %v", requests)
	}
}

func TestParseWikipediaDisplayVolumesReadsOrderedListStarts(t *testing.T) {
	var fixture strings.Builder
	fixture.WriteString(`<section aria-labelledby="Volumes"><table class="wikitable">`)
	fixture.WriteString(`<tr><th scope="row" id="vol1">1</th><td>Title</td></tr>`)
	fixture.WriteString(`<tr><td colspan="5"><ol start="1"><li>One</li><li>Two</li><li>Three</li><li>Four</li></ol>`)
	fixture.WriteString(`<ol start="5"><li>Five</li><li>Six</li><li>Seven</li></ol></td></tr>`)
	fixture.WriteString(`<tr><th scope="row" id="vol2">2</th><td>Title</td></tr>`)
	fixture.WriteString(`<tr><td colspan="5"><ol start="8"><li>Eight</li></ol></td></tr>`)
	fixture.WriteString(`</table></section>`)

	volumes, err := ParseWikipediaDisplayVolumes([]byte(fixture.String()))
	if err != nil {
		t.Fatal(err)
	}
	want := []Volume{{Volume: 1, Start: 1, End: 7}, {Volume: 2, Start: 8, End: 8}}
	if !reflect.DeepEqual(volumes, want) {
		t.Fatalf("volumes = %#v, want %#v", volumes, want)
	}
}

func TestParseWikipediaDisplayVolumesReadsNumberedItems(t *testing.T) {
	fixture := []byte(`<section aria-labelledby="Blue_Lock_volumes"><table class="wikitable">` +
		`<tr><th scope="row" id="vol1">1</th><td>2021</td></tr>` +
		`<tr><td colspan="5"><ul><li>1. "Dream"</li><li>2. "Moving In"</li><li>3. "Monster"</li><li>4. "Right Now"</li></ul></td></tr>` +
		`<tr><th scope="row" id="vol2">2</th><td>2021</td></tr>` +
		`<tr><td colspan="5"><ul><li>5. "Hero"</li><li>6. "Resolve"</li></ul></td></tr>` +
		`</table></section>`)

	volumes, err := ParseWikipediaDisplayVolumes(fixture)
	if err != nil {
		t.Fatal(err)
	}
	want := []Volume{{Volume: 1, Start: 1, End: 4}, {Volume: 2, Start: 5, End: 6}}
	if !reflect.DeepEqual(volumes, want) {
		t.Fatalf("volumes = %#v, want %#v", volumes, want)
	}
}

func TestParseWikipediaDisplayVolumesIgnoresNestedSections(t *testing.T) {
	// "Chapters not yet in tankōbon format" is nested inside the Volumes
	// section on some pages; its items must not extend the last volume.
	fixture := []byte(`<section aria-labelledby="Volumes"><table class="wikitable">` +
		`<tr><th scope="row" id="vol1">1</th></tr>` +
		`<tr><td colspan="5"><ul><li>1. "One"</li><li>2. "Two"</li></ul></td></tr>` +
		`</table>` +
		`<section aria-labelledby="Chapters_not_yet_in_tankōbon_format"><ul><li>3. "Three"</li></ul></section>` +
		`</section>`)

	volumes, err := ParseWikipediaDisplayVolumes(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(volumes) != 1 || volumes[0] != (Volume{Volume: 1, Start: 1, End: 2}) {
		t.Fatalf("volumes = %#v, want vol1 1-2 only", volumes)
	}
}

func TestParseWikipediaDisplayVolumesRejectsSectionsWithoutChapterLists(t *testing.T) {
	// Series whose Wikipedia volume table only lists release data (no
	// per-volume chapter lists) report no usable grouping.
	fixture := []byte(`<section aria-labelledby="Volumes"><table class="wikitable">` +
		`<tr><th scope="row" id="vol1">1</th><td>October 23, 2014</td><td>978-4-04-066884-0</td></tr>` +
		`<tr><th scope="row" id="vol2">2</th><td>March 23, 2015</td><td>978-4-04-067293-9</td></tr>` +
		`</table></section>`)

	if _, err := ParseWikipediaDisplayVolumes(fixture); !errors.Is(err, errNoWikipediaVolumeRows) {
		t.Fatalf("err = %v, want errNoWikipediaVolumeRows", err)
	}
}

func TestSelectWikipediaListTitle(t *testing.T) {
	tests := []struct {
		name       string
		candidates []string
		series     string
		want       string
	}{
		{
			name:       "prefers chapters",
			candidates: []string{"List of Foo episodes", "List of Foo volumes", "List of Foo chapters"},
			series:     "Foo Bar",
			want:       "List of Foo chapters",
		},
		{
			name:       "falls back to volumes",
			candidates: []string{"List of Foo characters", "List of Foo episodes", "List of Foo volumes"},
			series:     "Foo Bar",
			want:       "List of Foo volumes",
		},
		{
			name:       "ignores unrelated series",
			candidates: []string{"List of Unrelated chapters"},
			series:     "Foo Bar",
			want:       "",
		},
		{
			name:       "tolerates punctuation differences",
			candidates: []string{"List of One-Punch Man chapters"},
			series:     "One Punch Man",
			want:       "List of One-Punch Man chapters",
		},
		{
			name:       "matches series whose name extends the article title",
			candidates: []string{"List of Jujutsu Kaisen chapters"},
			series:     "Jujutsu Kaisen Indo",
			want:       "List of Jujutsu Kaisen chapters",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := selectWikipediaListTitle(test.candidates, test.series, map[string]bool{})
			if got != test.want {
				t.Fatalf("selectWikipediaListTitle = %q, want %q", got, test.want)
			}
		})
	}
}

func intString(value int) string {
	if value == 0 {
		return "0"
	}
	var data [20]byte
	pos := len(data)
	for value > 0 {
		pos--
		data[pos] = byte('0' + value%10)
		value /= 10
	}
	return string(data[pos:])
}
