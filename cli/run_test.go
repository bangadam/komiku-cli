package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bangadam/komiku-cli/komiku"
	"github.com/bangadam/komiku-cli/store"
)

func TestSelectChaptersRangeGapRawAndAmbiguity(t *testing.T) {
	chapters := []komiku.Chapter{
		{RawID: "001", Display: "1", URL: "one-a", Number: 1},
		{RawID: "01", Display: "1", URL: "one-b", Number: 1},
		{RawID: "3", Display: "3", URL: "three", Number: 3},
		{RawID: "271-5", Display: "271.5", URL: "extra", Number: 271.5},
	}
	if _, err := SelectChapters(chapters, "1-3,271.5"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous range accepted: %v", err)
	}
	selected, err := SelectChapters(chapters, "3,271.5")
	if err != nil || len(selected) != 2 {
		t.Fatalf("range/list intersection result=%#v err=%v", selected, err)
	}
	if _, err := SelectChapters(chapters, "1"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("same-display raw variants did not fail clearly: %v", err)
	}
	selected, err = SelectChapters(chapters, "001")
	if err != nil || len(selected) != 1 || selected[0].RawID != "001" {
		t.Fatalf("raw selector result=%#v err=%v", selected, err)
	}
}

func TestSelectVolumesRejectsAmbiguityOnlyInsideRequestedVolumes(t *testing.T) {
	chapters := []komiku.Chapter{
		{RawID: "001", Display: "1", URL: "one-a", Number: 1},
		{RawID: "01", Display: "1", URL: "one-b", Number: 1},
		{RawID: "2", Display: "2", URL: "two", Number: 2},
	}
	volumes := []komiku.Volume{{Volume: 1, Start: 1, End: 1}, {Volume: 2, Start: 2, End: 2}}
	jobs, err := SelectVolumes(chapters, volumes, "2")
	if err != nil || len(jobs) != 1 || jobs[0].Chapter.RawID != "2" {
		t.Fatalf("unrelated ambiguity blocked volume 2: jobs=%+v err=%v", jobs, err)
	}
	if _, err := SelectVolumes(chapters, volumes, "1"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("selected ambiguous volume was accepted: %v", err)
	}
}

func TestMainNoArgsReportsHeadlessUsageWithoutStaleTUIClaim(t *testing.T) {
	var stderr bytes.Buffer
	if code := Main(context.Background(), nil, io.Discard, &stderr, Dependencies{}); code != 2 {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(stderr.String(), "usage: komiku-cli dl") || strings.Contains(strings.ToLower(stderr.String()), "issues 01-13") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestInvalidSeriesURLStopsBeforeNetworkAndStore(t *testing.T) {
	var hits int
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		hits++
		return nil, errors.New("unexpected HTTP request")
	})}
	tests := []struct {
		name string
		url  string
	}{
		{name: "arbitrary host", url: "https://attacker.invalid/manga/series/"},
		{name: "arbitrary production port", url: "https://komiku.org:8443/manga/series/"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			output := filepath.Join(root, "downloads")
			var stderr bytes.Buffer
			code := Main(context.Background(), []string{
				"dl", test.url, "--ch", "1", "--no-tui", "--out", output,
			}, io.Discard, &stderr, Dependencies{
				HTTP:       httpClient,
				ConfigPath: filepath.Join(root, "missing-config.json"),
			})
			if code == 0 || hits != 0 {
				t.Fatalf("invalid URL exit=%d HTTP hits=%d stderr=%q", code, hits, stderr.String())
			}
			if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid URL created output store %q: %v", output, err)
			}
		})
	}
}

func TestEngineStatusesRetryGapResumeAndAudit(t *testing.T) {
	var mu sync.Mutex
	hits := make(map[string]int)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		hit := hits[r.URL.Path]
		mu.Unlock()
		switch r.URL.Path {
		case "/manga/series/":
			fmt.Fprint(w, `<a href="/different-chapter-1/"></a><a href="/different-chapter-2/"></a><a href="/different-chapter-3/"></a><a href="/different-chapter-4/"></a>`)
		case "/different-chapter-1/":
			fmt.Fprintf(w, `<img class="klazy" data-src="bad" src="%s/img/one.jpg"><img class="klazy" src="%s/img/two.png">`, server.URL, server.URL)
		case "/different-chapter-2/":
			fmt.Fprintf(w, `<img class="klazy" src="%s/img/good.jpg"><img class="klazy" src="%s/img/bad.jpg">`, server.URL, server.URL)
		case "/different-chapter-3/":
			fmt.Fprintf(w, `<img class="klazy" src="%s/img/bad-all.jpg">`, server.URL)
		case "/different-chapter-4/":
			fmt.Fprint(w, `<img class="other" src="ignored.jpg">`)
		case "/img/one.jpg", "/img/good.jpg":
			if r.Header.Get("Referer") == "" || r.Header.Get("User-Agent") == "" {
				t.Errorf("missing image headers: %v", r.Header)
			}
			_, _ = w.Write(append([]byte{0xff, 0xd8}, make([]byte, 11*1024)...))
		case "/img/two.png":
			if hit < 3 {
				fmt.Fprint(w, "<html>transient</html>")
			} else {
				_, _ = w.Write(append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 11*1024)...))
			}
		default:
			fmt.Fprint(w, "<html>bad</html>")
		}
	}))
	defer server.Close()
	root := t.TempDir()
	seriesStore, err := store.Open(root, "series")
	if err != nil {
		t.Fatal(err)
	}
	client := komiku.NewClient(server.Client(), 0)
	client.Sleep = func(context.Context, time.Duration) error { return nil }
	chapters, err := client.Discover(context.Background(), server.URL+"/manga/series/")
	if err != nil {
		t.Fatal(err)
	}
	jobs := make([]Job, len(chapters))
	for i, chapter := range chapters {
		jobs[i] = Job{Chapter: chapter, Flat: true}
	}
	firstSummary := (&Engine{Client: client, Store: seriesStore, Workers: 2}).Run(context.Background(), jobs)
	results := firstSummary.Results
	want := []Status{Done, Part, Fail, NoImg}
	for i := range want {
		if results[i].Status != want[i] {
			t.Fatalf("chapter %d status=%s errors=%v", i+1, results[i].Status, results[i].Errors)
		}
	}
	if hits["/img/two.png"] != 3 || hits["/img/bad.jpg"] != 4 || hits["/img/bad-all.jpg"] != 4 {
		t.Fatalf("retry accounting = %#v", hits)
	}
	badBefore := hits["/img/bad.jpg"]
	secondSummary := (&Engine{Client: client, Store: seriesStore, Workers: 1}).Run(context.Background(), []Job{jobs[0], jobs[1]})
	results = secondSummary.Results
	if results[0].Status != Done || results[1].Status != Part || hits["/img/one.jpg"] != 1 || hits["/img/good.jpg"] != 1 || hits["/img/bad.jpg"] != badBefore+4 {
		t.Fatalf("gap resume failed: results=%#v hits=%#v", results, hits)
	}
	if firstSummary.AuditPath == secondSummary.AuditPath {
		t.Fatalf("per-run logs were reused: %q", firstSummary.AuditPath)
	}
	data, _ := os.ReadFile(secondSummary.AuditPath)
	if !strings.Contains(string(data), "summary DONE=1 PART=1 FAIL=0 NOIMG=0 pages_ok=3 pages_failed=1") {
		t.Fatalf("audit summary = %s", data)
	}
}

func TestHeadlessFlatSmokeAndVolumeCachePrecedence(t *testing.T) {
	var imageHits int
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manga/fixture/":
			fmt.Fprint(w, `<a href="/prefix-chapter-1/"></a>`)
		case "/prefix-chapter-1/":
			fmt.Fprintf(w, `<img class="klazy" src="%s/page one">`, server.URL)
		case "/page one":
			imageHits++
			_, _ = w.Write(append([]byte{0xff, 0xd8}, make([]byte, 11*1024)...))
		default:
			t.Fatalf("unexpected network path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"dl", server.URL + "/manga/fixture/", "--ch", "1", "--no-tui", "--out", root, "--delay", "0s"}, &stdout, &stderr, Dependencies{HTTP: server.Client(), ConfigPath: filepath.Join(root, "missing-config.json"), Now: func() time.Time { return time.Unix(3, 0) }})
	if code != 0 {
		t.Fatalf("headless code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if imageHits != 1 || !strings.Contains(stdout.String(), "chapter 1: DONE") {
		t.Fatalf("smoke stdout=%s hits=%d", stdout.String(), imageHits)
	}
	if _, err := os.Stat(filepath.Join(root, "fixture", "chapter-001", "001.jpg")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "fixture", ".state.json")); err != nil {
		t.Fatal(err)
	}

	cachePath := filepath.Join(root, "fixture", ".volumes.json")
	cache := komiku.VolumeCache{Source: "manual", Volumes: []komiku.Volume{{Volume: 7, Start: 1, End: 1}}}
	cacheData, _ := json.Marshal(cache)
	if err := os.WriteFile(cachePath, cacheData, 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	code = Main(context.Background(), []string{"dl", server.URL + "/manga/fixture/", "--vol", "7", "--no-tui", "--out", root, "--delay", "0s"}, &stdout, &stderr, Dependencies{HTTP: server.Client(), ConfigPath: filepath.Join(root, "missing-config.json"), Now: func() time.Time { return time.Unix(4, 0) }})
	if code != 0 {
		t.Fatalf("cached volume run code=%d stderr=%s", code, stderr.String())
	}
	if imageHits != 1 {
		t.Fatalf("layout-aware DONE resume fetched image again: hits=%d", imageHits)
	}
	if _, err := os.Stat(filepath.Join(root, "fixture", "vol-07", "chapter-001", "001.jpg")); err != nil {
		t.Fatal(err)
	}
}

func TestHeadlessRawPackURLFirstSmoke(t *testing.T) {
	originalJPEG := append([]byte{0xff, 0xd8}, bytes.Repeat([]byte{0x31}, 11*1024)...)
	originalPNG := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x32}, 11*1024)...)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manga/fixture-pack/":
			fmt.Fprint(w, `<a href="/fixture-chapter-1/"></a>`)
		case "/fixture-chapter-1/":
			fmt.Fprintf(w, `<img class="klazy" src="%s/one.jpeg"><img class="klazy" src="%s/two.png">`, server.URL, server.URL)
		case "/one.jpeg":
			_, _ = w.Write(originalJPEG)
		case "/two.png":
			_, _ = w.Write(originalPNG)
		default:
			t.Fatalf("unexpected network path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	seriesDir := filepath.Join(root, "fixture-pack")
	if err := os.MkdirAll(seriesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cacheData, _ := json.Marshal(komiku.VolumeCache{Source: "fixture", Volumes: []komiku.Volume{{Volume: 1, Start: 1, End: 1}}})
	if err := os.WriteFile(filepath.Join(seriesDir, ".volumes.json"), cacheData, 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{
		"dl", server.URL + "/manga/fixture-pack/", "--vol", "1", "--no-tui", "--pack", "--preset", "raw", "--out", root, "--delay", "0s",
	}, &stdout, &stderr, Dependencies{HTTP: server.Client(), ConfigPath: filepath.Join(root, "missing-config.json")})
	if code != 0 {
		t.Fatalf("raw pack code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	archivePath := filepath.Join(seriesDir, "fixture-pack Volume 01.cbz")
	if !strings.Contains(stdout.String(), "packed: "+archivePath+" preset=raw") {
		t.Fatalf("pack status missing: %s", stdout.String())
	}
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if len(archive.File) != 2 {
		t.Fatalf("archive entries = %d", len(archive.File))
	}
	for index, expected := range []struct {
		name string
		body []byte
	}{{"Chapter 001/001.jpg", originalJPEG}, {"Chapter 001/002.png", originalPNG}} {
		entry := archive.File[index]
		if entry.Name != expected.name || entry.Method != zip.Store {
			t.Fatalf("entry %d name=%s method=%d", index, entry.Name, entry.Method)
		}
		reader, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil || !bytes.Equal(body, expected.body) {
			t.Fatalf("entry %s changed bytes: err=%v", entry.Name, err)
		}
	}
	if _, err := os.Stat(archivePath + ".part"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive part remains: %v", err)
	}
}

func TestHeadlessPackPersistedMediumAndExplicitTinyRawOverrides(t *testing.T) {
	original := encodedJPEG(t, 1800, 900)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manga/presets/":
			fmt.Fprint(w, `<a href="/presets-chapter-1/"></a>`)
		case "/presets-chapter-1/":
			fmt.Fprintf(w, `<img class="klazy" src="%s/page.jpg">`, server.URL)
		case "/page.jpg":
			_, _ = w.Write(original)
		default:
			t.Fatalf("unexpected network path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	for _, test := range []struct {
		name       string
		config     string
		flag       string
		wantPreset string
		wantWidth  int
		raw        bool
	}{
		{name: "persisted medium", config: `{"preset":"medium","image_delay":"0s"}`, wantPreset: "medium", wantWidth: 1600},
		{name: "explicit tiny", config: `{"preset":"medium","image_delay":"0s"}`, flag: "tiny", wantPreset: "tiny", wantWidth: 1200},
		{name: "explicit raw", config: `{"preset":"medium","image_delay":"0s"}`, flag: "raw", wantPreset: "raw", raw: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			configPath := filepath.Join(root, "config.json")
			if err := os.WriteFile(configPath, []byte(test.config), 0o644); err != nil {
				t.Fatal(err)
			}
			seriesDir := filepath.Join(root, "presets")
			if err := os.MkdirAll(seriesDir, 0o755); err != nil {
				t.Fatal(err)
			}
			cacheData, _ := json.Marshal(komiku.VolumeCache{Source: "fixture", Volumes: []komiku.Volume{{Volume: 1, Start: 1, End: 1}}})
			if err := os.WriteFile(filepath.Join(seriesDir, ".volumes.json"), cacheData, 0o644); err != nil {
				t.Fatal(err)
			}
			args := []string{"dl", server.URL + "/manga/presets/", "--vol", "1", "--no-tui", "--pack", "--out", root}
			if test.flag != "" {
				args = append(args, "--preset", test.flag)
			}
			var stdout, stderr bytes.Buffer
			if code := Main(context.Background(), args, &stdout, &stderr, Dependencies{HTTP: server.Client(), ConfigPath: configPath}); code != 0 {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String(), "preset="+test.wantPreset) {
				t.Fatalf("stdout=%s", stdout.String())
			}
			archive, err := zip.OpenReader(filepath.Join(seriesDir, "presets Volume 01.cbz"))
			if err != nil {
				t.Fatal(err)
			}
			defer archive.Close()
			body := readZipEntry(t, archive.File[0])
			if test.raw {
				if archive.File[0].Name != "Chapter 001/001.jpg" || !bytes.Equal(body, original) {
					t.Fatal("raw override changed original")
				}
			} else {
				decoded, err := jpeg.Decode(bytes.NewReader(body))
				if err != nil || decoded.Bounds().Dx() != test.wantWidth {
					t.Fatalf("bounds=%v err=%v", decoded.Bounds(), err)
				}
			}
		})
	}
}

func encodedJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: uint8(x + y), A: 255})
		}
	}
	var output bytes.Buffer
	if err := jpeg.Encode(&output, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func readZipEntry(t *testing.T, entry *zip.File) []byte {
	t.Helper()
	reader, err := entry.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestConfigCommandPersistsOutputWithoutDroppingExistingSettings(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "nested", "config.json")
	if err := SaveFileConfig(configPath, FileConfig{ImageDelay: "1s", Preset: "raw"}); err != nil {
		t.Fatal(err)
	}
	outputRoot := filepath.Join(root, "manga")
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"config", "--out", outputRoot}, &stdout, &stderr, Dependencies{ConfigPath: configPath})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	config, err := LoadFileConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if config.OutputRoot != outputRoot || config.ImageDelay != "1s" || config.Preset != "raw" {
		t.Fatalf("config=%#v", config)
	}
	if !strings.Contains(stdout.String(), "output root: "+outputRoot) {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestConfigCommandShowsResolvedOutputWithoutMutation(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	outputRoot := filepath.Join(root, "manga")
	if err := SaveFileConfig(configPath, FileConfig{OutputRoot: outputRoot}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"config"}, &stdout, &stderr, Dependencies{ConfigPath: configPath})
	if code != 0 || !strings.Contains(stdout.String(), "output root: "+outputRoot) {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	after, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("config mutated: err=%v before=%q after=%q", err, before, after)
	}
}

func TestInvalidConfigStopsBeforeNetwork(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"image_delay":"-1s"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls int
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, fmt.Errorf("must not be called")
	})}
	code := Main(context.Background(), []string{"dl", "https://komiku.org/manga/x/", "--ch", "1", "--flat", "--no-tui"}, io.Discard, io.Discard, Dependencies{HTTP: httpClient, ConfigPath: configPath})
	if code == 0 || calls != 0 {
		t.Fatalf("invalid config code=%d network calls=%d", code, calls)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
