package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bangadam/komiku-cli/komiku"
)

func writePageFile(t *testing.T, dir, name string, size int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Real, decodable PNG content, including fixtures below the old 10KB cutoff.
	data := readerTestPNG(t)
	if len(data) < size {
		data = append(data, make([]byte, size-len(data))...)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLocalPagesSortsByPageNumberAndSkipsBroken(t *testing.T) {
	dir := t.TempDir()
	writePageFile(t, dir, "010.jpg", 12*1024)
	writePageFile(t, dir, "002.jpg", 12*1024)
	writePageFile(t, dir, "001.png", 12*1024)
	if err := os.WriteFile(filepath.Join(dir, "003.jpg"), []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".DS_Store"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	pages := localPages(dir)
	if len(pages) != 3 {
		t.Fatalf("want 3 valid pages, got %d: %+v", len(pages), pages)
	}
	if pages[0].Index != 1 || filepath.Base(pages[0].Local) != "001.png" {
		t.Fatalf("first page = %+v", pages[0])
	}
	if pages[1].Index != 2 || pages[2].Index != 10 || filepath.Base(pages[1].Local) != "002.jpg" || filepath.Base(pages[2].Local) != "010.jpg" {
		t.Fatalf("page order = %q, %q", pages[1].Local, pages[2].Local)
	}
}

func TestFindLocalChapterDirHandlesFlatAndVolumeLayouts(t *testing.T) {
	root := t.TempDir()

	flat := filepath.Join(root, "flat-series", "chapter-005")
	writePageFile(t, flat, "001.jpg", 12*1024)
	if got := findLocalChapterDir(filepath.Join(root, "flat-series"), "5"); got != flat {
		t.Fatalf("flat lookup = %q, want %q", got, flat)
	}

	mapped := filepath.Join(root, "mapped-series", "vol-02", "chapter-014")
	writePageFile(t, mapped, "001.jpg", 12*1024)
	if got := findLocalChapterDir(filepath.Join(root, "mapped-series"), "14"); got != mapped {
		t.Fatalf("volume lookup = %q, want %q", got, mapped)
	}

	if got := findLocalChapterDir(filepath.Join(root, "missing"), "1"); got != "" {
		t.Fatalf("missing lookup = %q, want empty", got)
	}
}

func TestLocalChaptersOrdersFlatAndVolumeDirs(t *testing.T) {
	root := t.TempDir()
	writePageFile(t, filepath.Join(root, "chapter-010"), "001.jpg", 12*1024)
	writePageFile(t, filepath.Join(root, "chapter-002"), "001.jpg", 12*1024)
	writePageFile(t, filepath.Join(root, "vol-01", "chapter-001"), "001.jpg", 12*1024)
	writePageFile(t, filepath.Join(root, "chapter-002-raw-extra"), "001.jpg", 12*1024)

	chapters, err := localChapters(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(chapters) < 3 {
		t.Fatalf("want at least 3 chapters, got %d", len(chapters))
	}
	for index := 1; index < len(chapters); index++ {
		if chapters[index-1].Number > chapters[index].Number {
			t.Fatalf("chapters are not sorted: %+v", chapters)
		}
	}
	if chapters[0].Number != 1 {
		t.Fatalf("first chapter = %v, want 1", chapters[0].Number)
	}
}

func TestSelectReaderChapterSupportsSelectors(t *testing.T) {
	chapters := []komiku.Chapter{
		{Display: "1", Number: 1, URL: "u1"},
		{Display: "79.2", Number: 79.2, URL: "u2"},
		{Display: "180", Number: 180, URL: "u3"},
	}
	index, err := selectReaderChapter(chapters, "latest")
	if err != nil || index != len(chapters)-1 {
		t.Fatalf("latest = %d, %v", index, err)
	}
	index, err = selectReaderChapter(chapters, "first")
	if err != nil || index != 0 {
		t.Fatalf("first = %d, %v", index, err)
	}
	index, err = selectReaderChapter(chapters, "79.2")
	if err != nil || chapters[index].Number != 79.2 {
		t.Fatalf("79.2 = %d, %v", index, err)
	}
	if _, err := selectReaderChapter(chapters, "999"); err == nil {
		t.Fatal("want an error for a missing chapter")
	}
	if _, err := selectReaderChapter(chapters, "abc"); err == nil {
		t.Fatal("want an error for an invalid selector")
	}
}

func readerTestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	img.Set(1, 1, color.NRGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type readerTestTransport func(*http.Request) (*http.Response, error)

func (f readerTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// This fixture is an in-memory transport: no test can accidentally hit the live
// CDN or localhost. Image bodies are genuine PNGs, small enough to catch the old
// ValidResumeFile cutoff bug.
type readerTestFixture struct {
	root         string
	dependencies Dependencies
	image        []byte
	rawIDs       []string
	counts       map[string]int
	requests     map[string]int
	beforeImage  func(*http.Request)
	imageBody    func(*http.Request) io.ReadCloser
}

func newReaderTestFixture(t *testing.T) *readerTestFixture {
	t.Helper()
	root := t.TempDir()
	config := filepath.Join(root, "config.json")
	if err := os.WriteFile(config, []byte(`{"image_delay":"0s"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &readerTestFixture{root: root, image: readerTestPNG(t), rawIDs: []string{"001", "002", "003", "004", "79-2"}, counts: map[string]int{"003": 5}, requests: map[string]int{}}
	f.dependencies = Dependencies{ConfigPath: config, HTTP: &http.Client{Transport: readerTestTransport(func(r *http.Request) (*http.Response, error) {
		f.requests[r.URL.Path]++
		var content string
		switch {
		case r.URL.Path == "/manga/fixture/":
			for _, raw := range f.rawIDs {
				content += fmt.Sprintf(`<a href="/fixture-chapter-%s/"></a>`, raw)
			}
		case strings.HasPrefix(r.URL.Path, "/fixture-chapter-"):
			raw := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/fixture-chapter-"), "/")
			count, exists := f.counts[raw]
			if !exists {
				count = 2
			}
			for i := 1; i <= count; i++ {
				content += fmt.Sprintf(`<img class="klazy" src="https://komiku.org/images/%s/%d.png">`, raw, i)
			}
		case strings.HasPrefix(r.URL.Path, "/images/"):
			if f.beforeImage != nil {
				f.beforeImage(r)
			}
			body := io.NopCloser(bytes.NewReader(f.image))
			if f.imageBody != nil {
				body = f.imageBody(r)
			}
			return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: body, Request: r}, nil
		default:
			t.Errorf("unexpected request: %s", r.URL)
			return nil, errors.New("unexpected fixture URL")
		}
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(content)), Request: r}, nil
	})}}
	return f
}

func (f *readerTestFixture) run(ctx context.Context, writer io.Writer, args ...string) error {
	cmd := NewReadCommand(f.dependencies)
	cmd.SetOut(writer)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(append([]string{"https://komiku.org/manga/fixture/", "--out", f.root, "--json", "--ch", "3"}, args...))
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	return cmd.ExecuteContext(ctx)
}

type readerTestEvent struct {
	Kind    string          `json:"kind"`
	Page    int             `json:"page"`
	Total   json.RawMessage `json:"pages"`
	Local   string          `json:"local"`
	Bytes   int64           `json:"bytes"`
	Message string          `json:"message"`
}

func readerTestReports(t *testing.T, data []byte) ([]ReaderReport, []readerTestEvent) {
	t.Helper()
	var reports []ReaderReport
	var events []readerTestEvent
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("invalid NDJSON: %v\n%s", err, data)
		}
		var event readerTestEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
		if event.Kind == "report" {
			var report ReaderReport
			if err := json.Unmarshal(raw, &report); err != nil {
				t.Fatal(err)
			}
			reports = append(reports, report)
		}
	}
	return reports, events
}

func readerTestJSON(t *testing.T, data []byte) ReaderReport {
	t.Helper()
	var report ReaderReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("invalid report: %v\n%s", err, data)
	}
	return report
}

func TestReaderProgressBeforeDownloadsAndFinalCacheHit(t *testing.T) {
	f := newReaderTestFixture(t)
	var out bytes.Buffer
	f.beforeImage = func(r *http.Request) {
		reports, _ := readerTestReports(t, out.Bytes())
		if len(reports) != 1 || reports[0].PageCount != 5 || reports[0].Pages[0].Cached || len(reports[0].Chapters) != 5 {
			t.Fatalf("download began before initial report: %s", out.String())
		}
	}
	if err := f.run(context.Background(), &out, "--progress"); err != nil {
		t.Fatal(err)
	}
	reports, events := readerTestReports(t, out.Bytes())
	if len(reports) != 2 || len(events) != 7 || events[0].Kind != "report" || events[6].Kind != "report" {
		t.Fatalf("wrong progress shape: %s", out.String())
	}
	final := reports[1]
	if final.Source != "cache" || final.Bytes != int64(5*len(f.image)) || final.ChapterAt != 3 || final.Total != 5 {
		t.Fatalf("wrong final report: %+v", final)
	}
	for i, event := range events[1:6] {
		if event.Kind != "page_cached" || event.Page != i+1 || string(event.Total) != "5" || !filepath.IsAbs(event.Local) || event.Bytes != int64(len(f.image)) {
			t.Fatalf("bad cached event: %+v", event)
		}
	}
	for _, ch := range final.Chapters {
		if ch.Done {
			t.Fatalf("reading marked downloaded: %+v", ch)
		}
	}
	if _, err := os.Stat(filepath.Join(f.root, "fixture", ".state.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reader created state: %v", err)
	}
	f.beforeImage = func(r *http.Request) { t.Fatalf("cache hit fetched image %s", r.URL) }
	out.Reset()
	if err := f.run(context.Background(), &out, "--progress"); err != nil {
		t.Fatal(err)
	}
	reports, events = readerTestReports(t, out.Bytes())
	if len(reports) != 2 || len(events) != 2 || reports[0].Bytes != final.Bytes || reports[1].Bytes != final.Bytes {
		t.Fatalf("cache hit did not emit initial/final state: %s", out.String())
	}
	if f.requests["/fixture-chapter-003/"] != 1 {
		t.Fatalf("valid manifest refetched page list: %+v", f.requests)
	}
}

type readerBrokenBody struct {
	*bytes.Reader
}

func (r readerBrokenBody) Read(p []byte) (int, error) {
	if r.Len() == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	return r.Reader.Read(p)
}
func (readerBrokenBody) Close() error { return nil }

func TestReaderPartialFailedRetryAndOfflineCache(t *testing.T) {
	f := newReaderTestFixture(t)
	f.imageBody = func(r *http.Request) io.ReadCloser {
		if r.URL.Path == "/images/003/3.png" {
			return readerBrokenBody{bytes.NewReader(f.image[:20])}
		}
		return io.NopCloser(bytes.NewReader(f.image))
	}
	var out bytes.Buffer
	if err := f.run(context.Background(), &out, "--progress"); err != nil {
		t.Fatal(err) // Image failures are per-page, not a failed command.
	}
	reports, events := readerTestReports(t, out.Bytes())
	final := reports[len(reports)-1]
	if len(final.Pages) != 5 || final.PageCount != 5 || final.Pages[2].Cached || final.Pages[2].Error == "" || final.Pages[3].Index != 4 || !final.Pages[4].Cached || final.Source != "network" || final.Bytes != int64(4*len(f.image)) {
		t.Fatalf("partial cache hidden: %+v", final)
	}
	if events[3].Kind != "page_failed" || events[3].Page != 3 || string(events[3].Total) != "5" || events[3].Message == "" {
		t.Fatalf("missing page_failed: %+v", events)
	}
	f.dependencies.HTTP = &http.Client{Transport: readerTestTransport(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("offline attempted request %s", r.URL)
		return nil, errors.New("offline")
	})}
	out.Reset()
	if err := f.run(context.Background(), &out, "--offline", "--refresh"); err != nil {
		t.Fatal(err)
	}
	offline := readerTestJSON(t, out.Bytes())
	if offline.Source != "cache" || offline.PageCount != 5 || offline.Pages[2].Error == "" || offline.Pages[2].Cached || offline.Pages[3].Index != 4 || len(offline.Chapters) != 5 || offline.Chapters[4].Display != "79.2" {
		t.Fatalf("offline cache/nav lost: %+v", offline)
	}
	// Restore a fresh fixture transport but retain the first run's request count.
	retry := newReaderTestFixture(t)
	retry.root = f.root
	retry.requests = f.requests
	retry.beforeImage = func(r *http.Request) {
		if r.URL.Path != "/images/003/3.png" {
			t.Fatalf("retry fetched valid cached page %s", r.URL)
		}
	}
	out.Reset()
	if err := retry.run(context.Background(), &out, "--progress"); err != nil {
		t.Fatal(err)
	}
	reports, events = readerTestReports(t, out.Bytes())
	if len(events) != 3 || reports[1].Source != "cache" || reports[1].Bytes != int64(5*len(f.image)) || reports[1].Pages[2].Error != "" {
		t.Fatalf("retry failed: %s", out.String())
	}
	for i := 1; i <= 5; i++ {
		want := 1
		if i == 3 {
			want = 2
		}
		if f.requests[fmt.Sprintf("/images/003/%d.png", i)] != want {
			t.Fatalf("unexpected retry requests: %+v", f.requests)
		}
	}
}

type readerEventWriter struct {
	bytes.Buffer
	after func([]byte)
}

func (w *readerEventWriter) Write(data []byte) (int, error) {
	n, err := w.Buffer.Write(data)
	if w.after != nil {
		w.after(data)
	}
	return n, err
}

func TestReaderInterruptedCacheAndCancellation(t *testing.T) {
	f := newReaderTestFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &readerEventWriter{after: func(data []byte) {
		if bytes.Contains(data, []byte(`"kind":"page_cached"`)) {
			cancel()
		}
	}}
	if err := f.run(ctx, out, "--progress"); !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation, got %v", err)
	}
	reports, events := readerTestReports(t, out.Bytes())
	if len(reports) != 2 || events[len(events)-1].Kind != "report" || !reports[1].Pages[0].Cached || reports[1].Pages[1].Cached || reports[1].Pages[1].Error == "" {
		t.Fatalf("lost interrupted state: %s", out.String())
	}
	f.beforeImage = func(r *http.Request) { t.Fatalf("offline requested %s", r.URL) }
	var offline bytes.Buffer
	before := f.requests["/manga/fixture/"]
	if err := f.run(context.Background(), &offline, "--offline"); err != nil {
		t.Fatal(err)
	}
	if f.requests["/manga/fixture/"] != before {
		t.Fatal("offline fetched discovery")
	}
	report := readerTestJSON(t, offline.Bytes())
	if report.PageCount != 5 || report.Bytes != int64(len(f.image)) || !report.Pages[0].Cached || report.Pages[4].Cached {
		t.Fatalf("bad offline interrupted state: %+v", report)
	}
}

func TestReaderLibraryGapsDuplicatesAndCorruptImages(t *testing.T) {
	f := newReaderTestFixture(t)
	dir := filepath.Join(f.root, "fixture", "vol-01", "chapter-003")
	writePageFile(t, dir, "001.png", 0)
	writePageFile(t, dir, "003.jpg", 0)
	writePageFile(t, dir, "003.png", 0)
	writePageFile(t, dir, "005.png", 0)
	if err := os.WriteFile(filepath.Join(dir, "004.png"), f.image[:20], 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := f.run(context.Background(), &out, "--offline"); err != nil {
		t.Fatal(err)
	}
	report := readerTestJSON(t, out.Bytes())
	if report.PageCount != 5 || !report.Pages[0].Cached || !report.Pages[4].Cached {
		t.Fatalf("bad library pages: %+v", report)
	}
	for _, i := range []int{1, 2, 3} {
		if report.Pages[i].Cached || report.Pages[i].Index != i+1 || report.Pages[i].Error == "" {
			t.Fatalf("bad gap/corrupt/duplicate slot: %+v", report.Pages[i])
		}
	}
	f.beforeImage = func(r *http.Request) {
		if r.URL.Path == "/images/003/1.png" || r.URL.Path == "/images/003/5.png" {
			t.Fatalf("valid library refetched %s", r.URL)
		}
	}
	out.Reset()
	if err := f.run(context.Background(), &out, "--refresh"); err != nil {
		t.Fatal(err)
	}
	report = readerTestJSON(t, out.Bytes())
	if report.Source != "cache" || !report.Pages[3].Cached || report.Pages[3].Error != "" {
		t.Fatalf("library repair failed: %+v", report)
	}
	broken, _ := os.ReadFile(filepath.Join(dir, "004.png"))
	if !bytes.Equal(broken, f.image[:20]) {
		t.Fatal("reader overwrote existing library data")
	}
}

func TestReaderFractionalRawIdentityAndAmbiguousSelection(t *testing.T) {
	f := newReaderTestFixture(t)
	f.rawIDs = []string{"79-02", "79-2", "79-20"}
	for _, raw := range []string{"79-2", "79-20"} {
		var out bytes.Buffer
		if err := f.run(context.Background(), &out, "--ch", raw); err != nil {
			t.Fatal(err)
		}
		report := readerTestJSON(t, out.Bytes())
		if report.Chapter.Display != "79.2" || !strings.HasSuffix(report.CacheDir, "-raw-"+raw) {
			t.Fatalf("raw identity lost: %+v", report)
		}
	}
	var out bytes.Buffer
	if err := f.run(context.Background(), &out, "--ch", "79.2"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous fractional number accepted: %v", err)
	}
	if err := f.run(context.Background(), &out, "--ch", "79-20", "--offline"); err != nil {
		t.Fatal(err)
	}
	report := readerTestJSON(t, out.Bytes())
	if !strings.HasSuffix(report.Chapter.URL, "-79-20/") || report.Total != 3 || len(report.Chapters) != 3 {
		t.Fatalf("offline raw identity lost: %+v", report)
	}
	for _, selector := range []string{"first", "latest", "1", "NaN", "../1"} {
		if _, err := selectReaderChapter(nil, selector); err == nil {
			t.Fatalf("empty chapter list accepted selector %q", selector)
		}
	}
}

func TestReaderRefreshIgnoresCacheButPreservesLibrary(t *testing.T) {
	f := newReaderTestFixture(t)
	dir := filepath.Join(f.root, "fixture", "chapter-003")
	writePageFile(t, dir, "001.png", 0)
	state := []byte(`{"done":[1,2]}`)
	if err := os.WriteFile(filepath.Join(f.root, "fixture", ".state.json"), state, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := f.run(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	first := readerTestJSON(t, out.Bytes())
	if !first.Chapters[0].Done || !first.Chapters[1].Done || first.Chapters[2].Done {
		t.Fatalf("downloaded status changed: %+v", first.Chapters)
	}
	f.beforeImage = func(r *http.Request) {
		if r.URL.Path == "/images/003/1.png" {
			t.Fatal("refresh fetched valid library image")
		}
	}
	out.Reset()
	if err := f.run(context.Background(), &out, "--refresh", "--progress"); err != nil {
		t.Fatal(err)
	}
	reports, _ := readerTestReports(t, out.Bytes())
	if !reports[0].Pages[0].Cached || reports[0].Pages[1].Cached || reports[0].Source != "network" || reports[1].Pages[0].Local != first.Pages[0].Local {
		t.Fatalf("refresh did not distinguish sources: %+v", reports)
	}
	for i := 2; i <= 5; i++ {
		if f.requests[fmt.Sprintf("/images/003/%d.png", i)] != 2 {
			t.Fatalf("refresh reused reader bytes: %+v", f.requests)
		}
	}
	actual, _ := os.ReadFile(filepath.Join(f.root, "fixture", ".state.json"))
	if !bytes.Equal(actual, state) {
		t.Fatal("reader changed download state")
	}
}

func TestReaderRejectsUnsafePathsAndManifestIndices(t *testing.T) {
	for _, target := range []string{"series", "cache", "chapter", "volume", "page", "state", "manifest", "cached-page"} {
		t.Run(target, func(t *testing.T) {
			f := newReaderTestFixture(t)
			series := filepath.Join(f.root, "fixture")
			outside := t.TempDir()
			writePageFile(t, outside, "sentinel.png", 0)
			var link, destination string
			destination = outside
			switch target {
			case "series":
				link = series
			case "cache":
				link = filepath.Join(series, ".reader")
			case "chapter":
				link = filepath.Join(series, "chapter-003")
			case "volume":
				link = filepath.Join(series, "vol-01")
			case "page":
				link, destination = filepath.Join(series, "chapter-003", "001.png"), filepath.Join(outside, "sentinel.png")
			case "state":
				link, destination = filepath.Join(series, ".state.json"), filepath.Join(outside, "sentinel.png")
			case "manifest", "cached-page":
				var out bytes.Buffer
				if err := f.run(context.Background(), &out); err != nil {
					t.Fatal(err)
				}
				report := readerTestJSON(t, out.Bytes())
				link = filepath.Join(report.CacheDir, "manifest.json")
				if target == "cached-page" {
					link = report.Pages[0].Local
				}
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
				destination = filepath.Join(outside, "sentinel.png")
			}
			if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(destination, link); err != nil {
				t.Fatal(err)
			}
			if err := f.run(context.Background(), io.Discard, "--offline"); err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("unsafe %s accepted: %v", target, err)
			}
			entries, _ := os.ReadDir(outside)
			if len(entries) != 1 {
				t.Fatal("wrote through symlink")
			}
		})
	}
	for _, mutation := range []string{"index", "count", "file", "raw", "url", "navigation"} {
		t.Run("manifest-"+mutation, func(t *testing.T) {
			f := newReaderTestFixture(t)
			var out bytes.Buffer
			if err := f.run(context.Background(), &out); err != nil {
				t.Fatal(err)
			}
			report := readerTestJSON(t, out.Bytes())
			path := filepath.Join(report.CacheDir, "manifest.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var m readerManifest
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "index":
				m.Pages[1].Index = 1
			case "count":
				m.PageCount--
			case "file":
				m.Pages[0].File = "../../outside.png"
			case "raw":
				m.Chapter.RawID = "../../../outside"
			case "url":
				m.Pages[0].URL = "file:///etc/passwd"
			case "navigation":
				m.Chapters = nil
			}
			data, err = json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := f.run(context.Background(), io.Discard, "--offline"); err == nil {
				t.Fatalf("invalid %s manifest accepted", mutation)
			}
		})
	}
}

func TestReaderChecksumCorruptionRepairAndUnrecordedImage(t *testing.T) {
	f := newReaderTestFixture(t)
	var out bytes.Buffer
	if err := f.run(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	report := readerTestJSON(t, out.Bytes())
	path := filepath.Join(report.CacheDir, "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m readerManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	// Simulate dying between the image rename and the per-page manifest update.
	m.Pages[0].File, m.Pages[0].Bytes, m.Pages[0].SHA256 = "", 0, ""
	data, _ = json.Marshal(m)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	// Still-decodable changed bytes must fail the recorded digest, not merely
	// image magic. Failed repairs must keep this checksum for the next retry.
	if err := os.WriteFile(report.Pages[2].Local, append(f.image, 'x'), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := f.run(context.Background(), &out, "--offline"); err != nil {
		t.Fatal(err)
	}
	offline := readerTestJSON(t, out.Bytes())
	if !offline.Pages[0].Cached || offline.Pages[2].Cached || !strings.Contains(offline.Pages[2].Error, "checksum") {
		t.Fatalf("invalid recovery state: %+v", offline)
	}
	f.imageBody = func(r *http.Request) io.ReadCloser { return readerBrokenBody{bytes.NewReader(f.image[:20])} }
	out.Reset()
	if err := f.run(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := f.run(context.Background(), &out, "--offline"); err != nil {
		t.Fatal(err)
	}
	if readerTestJSON(t, out.Bytes()).Pages[2].Cached {
		t.Fatal("failed repair cleared checksum and adopted corrupt bytes")
	}
	f.imageBody = nil
	f.beforeImage = func(r *http.Request) {
		if r.URL.Path != "/images/003/3.png" {
			t.Fatalf("repair refetched usable page %s", r.URL)
		}
	}
	out.Reset()
	if err := f.run(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	if repaired := readerTestJSON(t, out.Bytes()); !repaired.Pages[2].Cached || repaired.Source != "cache" || repaired.Bytes != int64(5*len(f.image)) {
		t.Fatalf("repair failed: %+v", repaired)
	}
}

func TestReaderNoCacheEmptyListsAndWriterFailures(t *testing.T) {
	f := newReaderTestFixture(t)
	f.beforeImage = func(r *http.Request) { t.Fatalf("--cache=false fetched %s", r.URL) }
	var out bytes.Buffer
	if err := f.run(context.Background(), &out, "--cache=false", "--progress"); err != nil {
		t.Fatal(err)
	}
	reports, events := readerTestReports(t, out.Bytes())
	if len(events) != 2 || reports[1].Source != "network" || reports[1].PageCount != 5 {
		t.Fatalf("bad uncached report: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(f.root, "fixture")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("--cache=false wrote data: %v", err)
	}
	if err := f.run(context.Background(), io.Discard, "--offline"); err == nil {
		t.Fatal("offline nonexistent chapter succeeded")
	}
	f.counts["003"] = 0
	if err := f.run(context.Background(), io.Discard); err == nil {
		t.Fatal("empty page list succeeded")
	}
	f.rawIDs = nil
	if err := f.run(context.Background(), io.Discard); err == nil {
		t.Fatal("empty navigation succeeded")
	}
	f.rawIDs = []string{"003"}
	f.counts["003"] = 1
	if err := f.run(context.Background(), readerFailWriter{}, "--progress"); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("writer error lost: %v", err)
	}
}

type readerFailWriter struct{}

func (readerFailWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestReaderLegacyPartialCacheNeedsRealExpectedList(t *testing.T) {
	f := newReaderTestFixture(t)
	dir := filepath.Join(f.root, "fixture", ".reader", "chapter-003")
	writePageFile(t, dir, "001.png", 0)
	writePageFile(t, dir, "004.png", 0)
	f.beforeImage = func(r *http.Request) {
		if r.URL.Path == "/images/003/1.png" || r.URL.Path == "/images/003/4.png" {
			t.Fatalf("legacy image not reused: %s", r.URL)
		}
	}
	var out bytes.Buffer
	if err := f.run(context.Background(), &out, "--progress"); err != nil {
		t.Fatal(err)
	}
	reports, _ := readerTestReports(t, out.Bytes())
	if reports[0].PageCount != 5 || reports[0].Pages[1].Cached || !reports[0].Pages[3].Cached || reports[1].Bytes != int64(5*len(f.image)) {
		t.Fatalf("legacy partial cache considered complete: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "001.png")); err != nil {
		t.Fatal("legacy file was removed", err)
	}
	out.Reset()
	if err := f.run(context.Background(), &out, "--offline"); err != nil {
		t.Fatal(err)
	}
	if report := readerTestJSON(t, out.Bytes()); report.PageCount != 5 || report.Bytes != int64(5*len(f.image)) {
		t.Fatalf("legacy adoption not offline-readable: %+v", report)
	}
}

func TestReaderDistinctURLsWithSameRawIDDoNotAlias(t *testing.T) {
	f := newReaderTestFixture(t)
	f.rawIDs = []string{"003", "003"}
	transport := f.dependencies.HTTP.Transport
	f.dependencies.HTTP = &http.Client{Transport: readerTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/manga/fixture/" {
			return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`<a href="/fixture-chapter-003/"></a><a href="/other-chapter-003/"></a>`)), Request: r}, nil
		}
		if r.URL.Path == "/other-chapter-003/" {
			return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`<img class="klazy" src="https://komiku.org/images/other/1.png">`)), Request: r}, nil
		}
		return transport.RoundTrip(r)
	})}
	writePageFile(t, filepath.Join(f.root, "fixture", "chapter-003-raw-003"), "001.png", 0)
	var out bytes.Buffer
	if err := f.run(context.Background(), &out, "--ch", "003"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("duplicate raw selector accepted: %v", err)
	}
	if err := f.run(context.Background(), &out, "--ch", "https://komiku.org/fixture-chapter-003/"); err != nil {
		t.Fatal(err)
	}
	first := readerTestJSON(t, out.Bytes())
	out.Reset()
	if err := f.run(context.Background(), &out, "--ch", "https://komiku.org/other-chapter-003/"); err != nil {
		t.Fatal(err)
	}
	second := readerTestJSON(t, out.Bytes())
	if first.CacheDir == second.CacheDir || second.PageCount != 1 || first.PageCount != 5 || f.requests["/images/003/1.png"] != 1 || f.requests["/images/other/1.png"] != 1 {
		t.Fatalf("distinct URLs shared bytes: first=%+v second=%+v", first, second)
	}
}

func TestReaderOfflineRawFoldersAndPageIndexBounds(t *testing.T) {
	f := newReaderTestFixture(t)
	writePageFile(t, filepath.Join(f.root, "fixture", "chapter-079.2-raw-79-2"), "001.png", 0)
	writePageFile(t, filepath.Join(f.root, "fixture", "vol-01", "chapter-079.2-raw-79-20"), "002.png", 0)
	var out bytes.Buffer
	if err := f.run(context.Background(), &out, "--offline", "--ch", "79.2"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("offline raw folders merged: %v", err)
	}
	if err := f.run(context.Background(), &out, "--offline", "--ch", "79-20"); err != nil {
		t.Fatal(err)
	}
	report := readerTestJSON(t, out.Bytes())
	if report.PageCount != 2 || report.Total != 2 || report.Pages[0].Cached || !report.Pages[1].Cached || report.Pages[1].Index != 2 {
		t.Fatalf("offline fractional/gap handling failed: %+v", report)
	}
	writePageFile(t, filepath.Join(f.root, "fixture", "chapter-003"), "10001.png", 0)
	if err := f.run(context.Background(), io.Discard, "--offline"); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("unbounded index accepted: %v", err)
	}
}

func TestReaderUnsafeSeriesAndOnlineCachePaths(t *testing.T) {
	f := newReaderTestFixture(t)
	for _, slug := range []string{"..", "%2e%2e", "foo%5cbar", "foo%00bar", "foo%2fbar"} {
		cmd := NewReadCommand(f.dependencies)
		cmd.SetArgs([]string{"https://komiku.org/manga/" + slug + "/", "--out", f.root, "--offline", "--json"})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.ExecuteContext(context.Background()); err == nil {
			t.Fatalf("unsafe series slug %q accepted", slug)
		}
	}
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(f.root, "fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(f.root, "fixture", ".reader")); err != nil {
		t.Fatal(err)
	}
	if err := f.run(context.Background(), io.Discard); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("online cache symlink accepted: %v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("outside directory touched: %v %v", entries, err)
	}
}

func TestReaderCancellationDuringImageBodyLeavesAtomicPartialCache(t *testing.T) {
	f := newReaderTestFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.imageBody = func(r *http.Request) io.ReadCloser {
		if r.URL.Path == "/images/003/2.png" {
			return readerCancelBody{prefix: bytes.NewReader(f.image[:20]), cancel: cancel}
		}
		return io.NopCloser(bytes.NewReader(f.image))
	}
	var out bytes.Buffer
	if err := f.run(ctx, &out, "--progress"); !errors.Is(err, context.Canceled) {
		t.Fatalf("body cancellation lost: %v", err)
	}
	reports, events := readerTestReports(t, out.Bytes())
	final := reports[len(reports)-1]
	if events[len(events)-1].Kind != "report" || events[2].Kind != "page_failed" || events[2].Page != 2 || !final.Pages[0].Cached || final.Pages[1].Cached || final.Pages[1].Error == "" || final.Bytes != int64(len(f.image)) {
		t.Fatalf("bad cancelled report: %s", out.String())
	}
	entries, err := os.ReadDir(final.CacheDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".part") || strings.HasPrefix(entry.Name(), "002-") {
			t.Fatalf("partial image published: %s", entry.Name())
		}
	}
	out.Reset()
	if err := f.run(context.Background(), &out, "--offline"); err != nil {
		t.Fatal(err)
	}
	if offline := readerTestJSON(t, out.Bytes()); offline.PageCount != 5 || offline.Pages[1].Cached || !offline.Pages[0].Cached {
		t.Fatalf("cancelled cache cannot be read offline: %+v", offline)
	}
}

type readerCancelBody struct {
	prefix *bytes.Reader
	cancel context.CancelFunc
}

func (r readerCancelBody) Read(p []byte) (int, error) {
	if r.prefix.Len() != 0 {
		return r.prefix.Read(p)
	}
	r.cancel()
	return 0, context.Canceled
}
func (readerCancelBody) Close() error { return nil }

func TestReaderProgressWriterFailureStopsDownloads(t *testing.T) {
	f := newReaderTestFixture(t)
	writer := &readerStopWriter{}
	if err := f.run(context.Background(), writer, "--progress"); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("event writer failure lost: %v", err)
	}
	if f.requests["/images/003/1.png"] != 1 || f.requests["/images/003/2.png"] != 0 {
		t.Fatalf("downloads continued after writer failed: %+v", f.requests)
	}
	if writer.reports != 2 {
		t.Fatalf("final report was not attempted: %d", writer.reports)
	}
}

type readerStopWriter struct{ reports int }

func (w *readerStopWriter) Write(data []byte) (int, error) {
	if bytes.Contains(data, []byte(`"kind":"report"`)) {
		w.reports++
		return len(data), nil
	}
	return 0, io.ErrClosedPipe
}
