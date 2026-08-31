package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bangadam/komiku-cli/komiku"
	"github.com/bangadam/komiku-cli/store"
)

func TestStandalonePackUsesManifestOfflineAfterProcessRestart(t *testing.T) {
	seriesDir := filepath.Join(t.TempDir(), "offline-series")
	if err := os.MkdirAll(seriesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sources := make([]PackChapterSource, 0, 2)
	before := make(map[string][]byte)
	for number := 1; number <= 2; number++ {
		relative := fmt.Sprintf("chapter-%03d", number)
		writeManifestTestImage(t, filepath.Join(seriesDir, relative), "001.jpg")
		filename := filepath.Join(seriesDir, relative, "001.jpg")
		data, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		before[filename] = data
		sources = append(sources, PackChapterSource{Chapter: komiku.Chapter{Display: fmt.Sprint(number), Number: float64(number)}, Volume: 1, Dir: relative, ExpectedPages: 1, Complete: true})
	}
	if err := RecordRecoveredPackManifest(seriesDir, "offline-series", []komiku.Volume{{Volume: 1, Start: 1, End: 2}}, sources); err != nil {
		t.Fatal(err)
	}

	httpCalls := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		httpCalls++
		return nil, errors.New("normal standalone pack must remain offline")
	})}
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"pack", seriesDir, "--preset", "raw"}, &stdout, &stderr, Dependencies{HTTP: httpClient})
	if code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if httpCalls != 0 {
		t.Fatalf("normal manifest pack made %d HTTP requests", httpCalls)
	}
	if !strings.Contains(stdout.String(), "packed:") || !strings.Contains(stdout.String(), "preset=raw") {
		t.Fatalf("pack result was not reported: %q", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(seriesDir, "offline-series Volume 01.cbz")); err != nil {
		t.Fatal(err)
	}
	for filename, want := range before {
		got, err := os.ReadFile(filename)
		if err != nil {
			t.Fatalf("source image was moved: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("source image was rewritten: %s", filename)
		}
	}
	if _, err := os.Stat(filepath.Join(seriesDir, "vol-01")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("standalone pack materialized a mapped source tree: %v", err)
	}
}

func TestStandaloneManifestPackRejectsInvalidMagicBeforeArchiveCreation(t *testing.T) {
	seriesDir := filepath.Join(t.TempDir(), "invalid-series")
	sourceDir := filepath.Join(seriesDir, "chapter-001")
	writeManifestTestImage(t, sourceDir, "001.jpg")
	source := PackChapterSource{Chapter: komiku.Chapter{Display: "1", Number: 1}, Volume: 1, Dir: "chapter-001", ExpectedPages: 1, Complete: true}
	if err := RecordRecoveredPackManifest(seriesDir, "invalid-series", []komiku.Volume{{Volume: 1, Start: 1, End: 1}}, []PackChapterSource{source}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "001.jpg"), []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	httpCalls := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		httpCalls++
		return nil, errors.New("normal pack attempted HTTP")
	})}
	var stderr bytes.Buffer
	code := Main(context.Background(), []string{"pack", seriesDir, "--preset", "raw"}, io.Discard, &stderr, Dependencies{HTTP: client})
	if code == 0 || !strings.Contains(stderr.String(), "not a valid image") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if httpCalls != 0 {
		t.Fatalf("invalid manifest pack made %d HTTP requests", httpCalls)
	}
	if _, err := os.Stat(filepath.Join(seriesDir, "invalid-series Volume 01.cbz")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid source created an archive: %v", err)
	}
}

func TestStandalonePackWithoutManifestExplainsLegacyRecovery(t *testing.T) {
	seriesDir := newLegacySeries(t, []float64{1, 2}, []float64{1, 2})
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("normal pack must stay offline")
	})}
	var stderr bytes.Buffer
	code := Main(context.Background(), []string{"pack", seriesDir}, io.Discard, &stderr, Dependencies{HTTP: client})
	if code == 0 || requests != 0 {
		t.Fatalf("code=%d requests=%d stderr=%q", code, requests, stderr.String())
	}
	if !strings.Contains(stderr.String(), "legacy flat download") || !strings.Contains(stderr.String(), "komiku-cli pack ") || !strings.Contains(stderr.String(), "--recover-wikipedia") {
		t.Fatalf("recovery guidance missing: %q", stderr.String())
	}
}

func TestWikipediaRecoveryUsesOneRequestPersistsFlatSourcesAndThenPacksOffline(t *testing.T) {
	seriesDir := newLegacySeries(t, []float64{1, 2, 2.5}, []float64{1, 2, 2.5})
	requests := make([]string, 0, 1)
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.URL.String())
		if request.URL.Hostname() != "en.wikipedia.org" {
			t.Fatalf("recovery requested non-Wikipedia host %s", request.URL.Hostname())
		}
		return wikipediaFixtureResponse(request), nil
	})}

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"pack", seriesDir, "--recover-wikipedia", "--vol", "1", "--preset", "raw"}, &stdout, &stderr, Dependencies{HTTP: httpClient})
	if code != 0 {
		t.Fatalf("recovery code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	wantURL := "https://en.wikipedia.org/wiki/List_of_Sakamoto_Days_chapters"
	if !reflect.DeepEqual(requests, []string{wantURL}) {
		t.Fatalf("recovery requests=%#v, want exactly one %s", requests, wantURL)
	}
	if !strings.Contains(stdout.String(), "Wikipedia source: "+wantURL) || !strings.Contains(stdout.String(), "ignored local chapters outside --vol: 2.5") {
		t.Fatalf("recovery reporting=%q", stdout.String())
	}
	manifest, err := LoadPackManifest(seriesDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Chapters) != 2 || manifest.Chapters[0].IdentityProvenance != "recovered-local" || manifest.Chapters[0].SourceDir != "chapter-001" || manifest.Chapters[1].SourceDir != "chapter-002" {
		t.Fatalf("recovered manifest did not retain direct flat sources: %+v", manifest)
	}
	if _, err := os.Stat(filepath.Join(seriesDir, "vol-01")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery copied or linked images into a mapped tree: %v", err)
	}

	if err := os.Remove(filepath.Join(seriesDir, "sakamoto-days Volume 01.cbz")); err != nil {
		t.Fatal(err)
	}
	requests = nil
	stdout.Reset()
	stderr.Reset()
	code = Main(context.Background(), []string{"pack", seriesDir, "--preset", "raw"}, &stdout, &stderr, Dependencies{HTTP: httpClient})
	if code != 0 {
		t.Fatalf("second offline pack code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if len(requests) != 0 {
		t.Fatalf("second manifest pack made HTTP requests: %#v", requests)
	}
}

func TestWikipediaRecoveryCompleteModePacksOnlyCompleteVolumes(t *testing.T) {
	seriesDir := newLegacySeries(t, []float64{1, 2, 2.5}, []float64{1, 2, 2.5})
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return wikipediaFixtureResponse(request), nil
	})}
	var output bytes.Buffer
	err := PackDownloaded(context.Background(), seriesDir, PackDownloadedOptions{
		Preset: "raw", RecoverWikipedia: true, RecoverComplete: true, HTTP: client, Output: &output,
	})
	if err != nil || requests != 1 {
		t.Fatalf("err=%v requests=%d output=%q", err, requests, output.String())
	}
	if !strings.Contains(output.String(), "left unchanged because they are outside complete volumes: 2.5") {
		t.Fatalf("ignored extra not reported: %q", output.String())
	}
	if _, err := os.Stat(filepath.Join(seriesDir, "sakamoto-days Volume 01.cbz")); err != nil {
		t.Fatal(err)
	}
}

func TestWikipediaRecoveryRefusesExistingManifestWithoutHTTP(t *testing.T) {
	seriesDir := newLegacySeries(t, []float64{1, 2}, []float64{1, 2})
	if err := os.WriteFile(PackManifestPath(seriesDir), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("unexpected request")
	})}
	var stderr bytes.Buffer
	code := Main(context.Background(), []string{"pack", seriesDir, "--recover-wikipedia"}, io.Discard, &stderr, Dependencies{HTTP: client})
	if code == 0 || requests != 0 || !strings.Contains(stderr.String(), "already exists") {
		t.Fatalf("code=%d requests=%d stderr=%q", code, requests, stderr.String())
	}
}

func TestWikipediaRecoveryDisablesRedirects(t *testing.T) {
	seriesDir := newLegacySeries(t, []float64{1, 2}, []float64{1, 2})
	requests := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			response := &http.Response{StatusCode: http.StatusFound, Status: "302 Found", Header: make(http.Header), Body: io.NopCloser(strings.NewReader("redirect")), Request: request}
			response.Header.Set("Location", "https://en.wikipedia.org/wiki/redirect-target")
			return response, nil
		}
		return wikipediaFixtureResponse(request), nil
	})}
	var stderr bytes.Buffer
	code := Main(context.Background(), []string{"pack", seriesDir, "--recover-wikipedia", "--vol", "1", "--preset", "raw"}, io.Discard, &stderr, Dependencies{HTTP: httpClient})
	if code == 0 || requests != 1 || !strings.Contains(stderr.String(), "302 Found") {
		t.Fatalf("redirect recovery code=%d requests=%d stderr=%q", code, requests, stderr.String())
	}
	if _, err := os.Stat(PackManifestPath(seriesDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("redirect response persisted a manifest: %v", err)
	}
}

func TestWikipediaRecoveryRollsBackNewArchivesAfterLaterVolumeFailure(t *testing.T) {
	seriesDir := newLegacySeries(t, []float64{1, 2}, []float64{1, 2})
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return twoVolumeWikipediaFixtureResponse(request), nil
	})}
	previous := executePreparedPack
	defer func() { executePreparedPack = previous }()
	executePreparedPack = func(ctx context.Context, plan PackPlan) ([]PackOutcome, error) {
		first := PackPlan{Preset: plan.Preset, Volumes: plan.Volumes[:1]}
		outcomes, err := PackPreparedVolumes(ctx, first)
		if err != nil {
			return outcomes, err
		}
		outcomes = append(outcomes, PackOutcome{Volume: plan.Volumes[1].Number, Err: errors.New("injected later-volume failure")})
		return outcomes, nil
	}
	var stderr bytes.Buffer
	code := Main(context.Background(), []string{"pack", seriesDir, "--recover-wikipedia", "--preset", "raw"}, io.Discard, &stderr, Dependencies{HTTP: client})
	if code == 0 || !strings.Contains(stderr.String(), "injected later-volume failure") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	for _, filename := range []string{PackManifestPath(seriesDir), filepath.Join(seriesDir, "sakamoto-days Volume 01.cbz"), filepath.Join(seriesDir, "sakamoto-days Volume 02.cbz")} {
		if _, err := os.Stat(filename); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed recovery published %s: %v", filename, err)
		}
	}
}

func TestWikipediaRecoveryRefusesPreExistingArchiveWithoutReplacingIt(t *testing.T) {
	seriesDir := newLegacySeries(t, []float64{1, 2}, []float64{1, 2})
	archive := filepath.Join(seriesDir, "sakamoto-days Volume 01.cbz")
	want := []byte("pre-existing archive")
	if err := os.WriteFile(archive, want, 0o644); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return wikipediaFixtureResponse(request), nil
	})}
	var stderr bytes.Buffer
	code := Main(context.Background(), []string{"pack", seriesDir, "--recover-wikipedia", "--vol", "1", "--preset", "raw"}, io.Discard, &stderr, Dependencies{HTTP: client})
	if code == 0 || !strings.Contains(stderr.String(), "pre-existing archive") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	got, err := os.ReadFile(archive)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("pre-existing archive changed: got=%q err=%v", got, err)
	}
	if _, err := os.Stat(PackManifestPath(seriesDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused recovery wrote manifest: %v", err)
	}
}

func TestWikipediaRecoveryRejectsUntrustedLegacyLayoutsBeforeWriting(t *testing.T) {
	tests := []struct {
		name         string
		prepare      func(*testing.T) string
		args         []string
		want         string
		wantRequests int
	}{
		{
			name: "partial requested volume",
			prepare: func(t *testing.T) string {
				return newLegacySeries(t, []float64{1}, []float64{1})
			},
			args: []string{"--vol", "1"}, want: "partial", wantRequests: 1,
		},
		{
			name: "ambiguous raw identity",
			prepare: func(t *testing.T) string {
				root := newLegacySeries(t, []float64{1, 2}, []float64{1, 2})
				writeManifestTestImage(t, filepath.Join(root, "chapter-001-raw-alt"), "001.jpg")
				return root
			},
			args: []string{"--vol", "1"}, want: "ambiguous raw identity", wantRequests: 0,
		},
		{
			name: "invalid image magic",
			prepare: func(t *testing.T) string {
				root := newLegacySeries(t, []float64{1, 2}, []float64{1, 2})
				if err := os.WriteFile(filepath.Join(root, "chapter-002", "001.jpg"), []byte("not an image"), 0o644); err != nil {
					t.Fatal(err)
				}
				return root
			},
			args: []string{"--vol", "1"}, want: "not a valid image", wantRequests: 1,
		},
		{
			name: "state claims missing folder",
			prepare: func(t *testing.T) string {
				return newLegacySeries(t, []float64{1}, []float64{1, 2})
			},
			args: []string{"--vol", "1"}, want: "has no canonical flat chapter folder", wantRequests: 0,
		},
		{
			name: "folder is not DONE",
			prepare: func(t *testing.T) string {
				return newLegacySeries(t, []float64{1, 2}, []float64{1})
			},
			args: []string{"--vol", "1"}, want: "chapter 2 is not DONE", wantRequests: 1,
		},
		{
			name: "noncanonical folder",
			prepare: func(t *testing.T) string {
				root := newLegacySeries(t, []float64{1, 2}, []float64{1, 2})
				if err := os.Rename(filepath.Join(root, "chapter-001"), filepath.Join(root, "chapter-01")); err != nil {
					t.Fatal(err)
				}
				return root
			},
			args: []string{"--vol", "1"}, want: "not canonical", wantRequests: 0,
		},
		{
			name: "chapter symlink escape",
			prepare: func(t *testing.T) string {
				root := newLegacySeries(t, []float64{2}, []float64{1, 2})
				outside := filepath.Join(t.TempDir(), "chapter-001")
				writeManifestTestImage(t, outside, "001.jpg")
				if err := os.Symlink(outside, filepath.Join(root, "chapter-001")); err != nil {
					t.Fatal(err)
				}
				return root
			},
			args: []string{"--vol", "1"}, want: "is a symlink", wantRequests: 0,
		},
		{
			name: "unscoped extra",
			prepare: func(t *testing.T) string {
				return newLegacySeries(t, []float64{1, 2, 2.5}, []float64{1, 2, 2.5})
			},
			want: "specify --vol to scope recovery", wantRequests: 1,
		},
		{
			name: "strict state schema",
			prepare: func(t *testing.T) string {
				root := newLegacySeries(t, []float64{1, 2}, []float64{1, 2})
				if err := os.WriteFile(filepath.Join(root, ".state.json"), []byte(`{"done":[1,2],"done":[1,2]}`), 0o644); err != nil {
					t.Fatal(err)
				}
				return root
			},
			args: []string{"--vol", "1"}, want: "duplicate object key", wantRequests: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			seriesDir := test.prepare(t)
			requests := 0
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				requests++
				return wikipediaFixtureResponse(request), nil
			})}
			args := append([]string{"pack", seriesDir, "--recover-wikipedia", "--preset", "raw"}, test.args...)
			var stderr bytes.Buffer
			code := Main(context.Background(), args, io.Discard, &stderr, Dependencies{HTTP: client})
			if code == 0 || !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("code=%d stderr=%q, want %q", code, stderr.String(), test.want)
			}
			if requests != test.wantRequests {
				t.Fatalf("requests=%d, want %d", requests, test.wantRequests)
			}
			if _, err := os.Stat(PackManifestPath(seriesDir)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed recovery wrote a manifest: %v", err)
			}
			archives, err := filepath.Glob(filepath.Join(seriesDir, "*.cbz"))
			if err != nil {
				t.Fatal(err)
			}
			if len(archives) != 0 {
				t.Fatalf("failed recovery wrote archives: %#v", archives)
			}
		})
	}
}

func newLegacySeries(t *testing.T, folders, done []float64) string {
	t.Helper()
	seriesDir := filepath.Join(t.TempDir(), "sakamoto-days")
	if err := os.MkdirAll(seriesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, number := range folders {
		display := store.FormatChapter(fmt.Sprint(number))
		writeManifestTestImage(t, filepath.Join(seriesDir, "chapter-"+display), "001.jpg")
	}
	values := make([]string, 0, len(done))
	for _, number := range done {
		values = append(values, fmt.Sprint(number))
	}
	if err := os.WriteFile(filepath.Join(seriesDir, ".state.json"), []byte(`{"done":[`+strings.Join(values, ",")+`]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return seriesDir
}

func twoVolumeWikipediaFixtureResponse(request *http.Request) *http.Response {
	body := `<section aria-labelledby="Volumes"><h3 id="Volumes">Volumes</h3><table class="wikitable"><tr><th scope="row" id="vol1">1</th><td><ul><li>Days 1: One</li></ul></td></tr><tr><th scope="row" id="vol2">2</th><td><ul><li>Days 2: Two</li></ul></td></tr></table></section>`
	return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

func wikipediaFixtureResponse(request *http.Request) *http.Response {
	body := `<section aria-labelledby="Volumes"><h3 id="Volumes">Volumes</h3><table class="wikitable"><tr><th scope="row" id="vol1">1</th><td><ul><li>Days 1: One</li><li>Days 2: Two</li></ul></td></tr></table></section>`
	return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}
}
