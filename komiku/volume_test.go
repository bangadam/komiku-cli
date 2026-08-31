package komiku

import (
	"context"
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
func TestFetchWikipediaDisplayVolumesDoesNotFallBack(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"no mapping", `<section aria-labelledby="Volumes"><p>No usable rows</p></section>`, false},
		{"invalid mapping", `<section aria-labelledby="Volumes"><th scope="row" id="vol1">1</th></section>`, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			client := NewClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				requests++
				return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.body)), Request: request}, nil
			})}, 0)

			volumes, err := client.FetchWikipediaDisplayVolumes(context.Background(), "Missing Series")
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
			if !test.wantErr && len(volumes) != 0 {
				t.Fatalf("no-map volumes = %#v", volumes)
			}
			if requests != 1 {
				t.Fatalf("display lookup made %d requests, want exactly one Wikipedia request", requests)
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
