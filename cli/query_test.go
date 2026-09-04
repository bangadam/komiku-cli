package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func searchFixtureServer(t *testing.T, hits *int) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		switch {
		case r.URL.Path == "/" && r.URL.Query().Get("post_type") == "manga":
			fmt.Fprint(w, `<a href="/manga/frieren/"><h4>Frieren</h4></a><a href="/manga/sakamoto-days/"><h4>Sakamoto Days</h4></a>`)
		case r.URL.Path == "/" && r.URL.Query().Get("s") != "":
			fmt.Fprint(w, `<div class="no-results">Nothing found</div>`)
		default:
			t.Fatalf("unexpected search path %s", r.URL.String())
		}
	}))
	return server
}

func TestSearchCommandTextAndJSON(t *testing.T) {
	var hits int
	server := searchFixtureServer(t, &hits)
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"search", "frieren"}, &stdout, &stderr, Dependencies{HTTP: server.Client(), BaseURL: server.URL})
	if code != 0 {
		t.Fatalf("search code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Frieren\t") {
		t.Fatalf("text output=%s", stdout.String())
	}
	if hits == 0 {
		t.Fatal("search performed no requests")
	}

	stdout.Reset()
	stderr.Reset()
	code = Main(context.Background(), []string{"search", "frieren", "--json"}, &stdout, &stderr, Dependencies{HTTP: server.Client(), BaseURL: server.URL})
	if code != 0 {
		t.Fatalf("search --json code=%d stderr=%s", code, stderr.String())
	}
	var results []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout.String())
	}
	if len(results) != 2 || results[0]["Title"] != "Frieren" {
		t.Fatalf("json results=%#v", results)
	}
	if results[1]["Slug"] != "sakamoto-days" {
		t.Fatalf("json slugs=%#v", results)
	}
}

func chapterListServer(t *testing.T, imageHits *int) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manga/fixture/":
			fmt.Fprint(w, `<a href="/prefix-chapter-1/"></a><a href="/prefix-chapter-2/"></a><a href="/prefix-chapter-3/"></a>`)
		default:
			if strings.HasPrefix(r.URL.Path, "/prefix-chapter-") {
				fmt.Fprintf(w, `<img class="klazy" src="%s/page.jpg">`, server.URL)
				return
			}
			if r.URL.Path == "/page.jpg" {
				mu.Lock()
				*imageHits++
				mu.Unlock()
				_, _ = w.Write(append([]byte{0xff, 0xd8}, make([]byte, 11*1024)...))
				return
			}
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	return server
}

func TestDownloadAllThenMissingSkipsDoneChapters(t *testing.T) {
	var imageHits int
	server := chapterListServer(t, &imageHits)
	defer server.Close()
	root := t.TempDir()
	configPath := filepath.Join(root, "missing-config.json")
	dependencies := Dependencies{HTTP: server.Client(), ConfigPath: configPath, Now: func() time.Time { return time.Unix(3, 0) }}

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"dl", server.URL + "/manga/fixture/", "--ch", "all", "--no-tui", "--out", root, "--delay", "0s"}, &stdout, &stderr, dependencies)
	if code != 0 {
		t.Fatalf("all code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if imageHits != 3 {
		t.Fatalf("expected 3 image fetches, got %d", imageHits)
	}
	if !strings.Contains(stdout.String(), "summary DONE=3") {
		t.Fatalf("summary line=%s", stdout.String())
	}

	imageHits = 0
	stdout.Reset()
	stderr.Reset()
	code = Main(context.Background(), []string{"dl", server.URL + "/manga/fixture/", "--ch", "missing", "--no-tui", "--out", root, "--delay", "0s"}, &stdout, &stderr, dependencies)
	if code != 0 {
		t.Fatalf("missing code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no missing chapters: fixture is already complete") {
		t.Fatalf("missing output=%s", stdout.String())
	}
	if imageHits != 0 {
		t.Fatalf("complete series refetched pages: hits=%d", imageHits)
	}
}

func TestDownloadMissingFillsGapsOnly(t *testing.T) {
	var imageHits int
	server := chapterListServer(t, &imageHits)
	defer server.Close()
	root := t.TempDir()
	configPath := filepath.Join(root, "missing-config.json")
	dependencies := Dependencies{HTTP: server.Client(), ConfigPath: configPath, Now: func() time.Time { return time.Unix(3, 0) }}

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"dl", server.URL + "/manga/fixture/", "--ch", "1-2", "--no-tui", "--out", root, "--delay", "0s"}, &stdout, &stderr, dependencies)
	if code != 0 {
		t.Fatalf("initial code=%d stderr=%s", code, stderr.String())
	}
	initialHits := imageHits

	stdout.Reset()
	stderr.Reset()
	code = Main(context.Background(), []string{"dl", server.URL + "/manga/fixture/", "--ch", "missing", "--no-tui", "--json", "--out", root, "--delay", "0s"}, &stdout, &stderr, dependencies)
	if code != 0 {
		t.Fatalf("missing json code=%d stderr=%s", code, stderr.String())
	}
	var report DownloadReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("invalid JSON report: %v\n%s", err, stdout.String())
	}
	if report.Requested != 1 || report.Counts[Done] != 1 {
		t.Fatalf("report=%#v", report)
	}
	if report.Results[0].Chapter != "3" {
		t.Fatalf("expected only chapter 3, got %q", report.Results[0].Chapter)
	}
	// Chapter 3 needs 1 chapter page + 1 image; chapters 1-2 are done.
	if imageHits-initialHits != 1 {
		t.Fatalf("gap fill fetched %d images, expected 1", imageHits-initialHits)
	}
	if report.MappedVolumes != nil {
		t.Fatalf("flat runs must not report mapped volumes: %#v", report.MappedVolumes)
	}
}


func TestInfoCommandMarksDoneChapters(t *testing.T) {
	var imageHits int
	server := chapterListServer(t, &imageHits)
	defer server.Close()
	root := t.TempDir()
	configPath := filepath.Join(root, "missing-config.json")
	dependencies := Dependencies{HTTP: server.Client(), ConfigPath: configPath}

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"dl", server.URL + "/manga/fixture/", "--ch", "1", "--no-tui", "--out", root, "--delay", "0s"}, &stdout, &stderr, dependencies)
	if code != 0 {
		t.Fatalf("setup dl code=%d stderr=%s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = Main(context.Background(), []string{"info", server.URL + "/manga/fixture/", "--out", root}, &stdout, &stderr, dependencies)
	if code != 0 {
		t.Fatalf("info code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "fixture  chapters=3 done=1") {
		t.Fatalf("info header=%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "[x] 1") || !strings.Contains(stdout.String(), "[ ] 2") {
		t.Fatalf("info markers missing: %s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = Main(context.Background(), []string{"info", server.URL + "/manga/fixture/", "--out", root, "--json"}, &stdout, &stderr, dependencies)
	if code != 0 {
		t.Fatalf("info json code=%d stderr=%s", code, stderr.String())
	}
	var info SeriesInfo
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout.String())
	}
	if info.Series != "fixture" || len(info.Chapters) != 3 || info.DoneCount != 1 {
		t.Fatalf("info json=%#v", info)
	}
	if !info.Chapters[0].Done || info.Chapters[1].Done {
		t.Fatalf("done flags=%#v", info.Chapters)
	}
}

func TestVersionFlagExitsCleanly(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"--version"}, &stdout, &stderr, Dependencies{})
	if code != 0 {
		t.Fatalf("version code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "version") {
		t.Fatalf("version output=%s", stdout.String())
	}
}
