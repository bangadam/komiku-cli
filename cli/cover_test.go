package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// coverFixtureServer serves a series page plus its cover image.
func coverFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/manga/"):
			fmt.Fprint(w, `<meta property="og:image" content="/covers/fixture.jpg">`)
		case strings.HasPrefix(r.URL.Path, "/covers/"):
			_, _ = w.Write(append([]byte{0xff, 0xd8}, make([]byte, 64)...))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	return server
}

func TestCoverCommandSavesIntoSeriesDirectory(t *testing.T) {
	server := coverFixtureServer(t)
	defer server.Close()
	root := t.TempDir()
	dependencies := Dependencies{HTTP: server.Client(), BaseURL: server.URL, ConfigPath: filepath.Join(root, "config.json")}

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"cover", server.URL + "/manga/fixture/", "--out", root, "--json"}, &stdout, &stderr, dependencies)
	if code != 0 {
		t.Fatalf("cover code=%d stderr=%s", code, stderr.String())
	}
	var reports []CoverReport
	if err := json.Unmarshal(stdout.Bytes(), &reports); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout.String())
	}
	if len(reports) != 1 || reports[0].Error != "" || reports[0].Bytes == 0 {
		t.Fatalf("reports=%+v", reports)
	}
	path := filepath.Join(root, "fixture", "cover.jpg")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cover not saved: %v", err)
	}
}

func TestCoverCommandSkipsExistingUnlessForced(t *testing.T) {
	server := coverFixtureServer(t)
	defer server.Close()
	root := t.TempDir()
	dependencies := Dependencies{HTTP: server.Client(), BaseURL: server.URL, ConfigPath: filepath.Join(root, "config.json")}
	if err := os.MkdirAll(filepath.Join(root, "fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(root, "fixture", "cover.png")
	if err := os.WriteFile(existing, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"cover", server.URL + "/manga/fixture/", "--out", root, "--json"}, &stdout, &stderr, dependencies)
	if code != 0 {
		t.Fatalf("cover code=%d stderr=%s", code, stderr.String())
	}
	var reports []CoverReport
	if err := json.Unmarshal(stdout.Bytes(), &reports); err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || !reports[0].Skipped {
		t.Fatalf("reports=%+v", reports)
	}
	data, err := os.ReadFile(existing)
	if err != nil || string(data) != "keep" {
		t.Fatalf("existing cover was overwritten: %q err=%v", data, err)
	}

	stdout.Reset()
	stderr.Reset()
	code = Main(context.Background(), []string{"cover", server.URL + "/manga/fixture/", "--out", root, "--force"}, &stdout, &stderr, dependencies)
	if code != 0 {
		t.Fatalf("force code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, "fixture", "cover.jpg")); err != nil {
		t.Fatalf("forced cover not saved: %v", err)
	}
	if _, err := os.Stat(existing); !os.IsNotExist(err) {
		t.Fatalf("stale cover.png survived: %v", err)
	}
}

func TestCoverCommandAllWalksTheLibrary(t *testing.T) {
	server := coverFixtureServer(t)
	defer server.Close()
	root := t.TempDir()
	for _, slug := range []string{"fixture", "other"} {
		dir := filepath.Join(root, slug)
		if err := os.MkdirAll(filepath.Join(dir, "chapter-001"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dependencies := Dependencies{HTTP: server.Client(), BaseURL: server.URL, ConfigPath: filepath.Join(root, "config.json")}

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"cover", "--all", "--out", root, "--json"}, &stdout, &stderr, dependencies)
	if code != 0 {
		t.Fatalf("cover --all code=%d stderr=%s", code, stderr.String())
	}
	var reports []CoverReport
	if err := json.Unmarshal(stdout.Bytes(), &reports); err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 {
		t.Fatalf("expected both series, got %+v", reports)
	}
	for _, report := range reports {
		if report.Error != "" {
			t.Fatalf("%s failed: %s", report.Slug, report.Error)
		}
	}
}

func TestCoverAllPrefersManifestAndIgnoresLegacyURLs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manga/original/", "/manga/fallback/":
			fmt.Fprint(w, `<meta property="og:image" content="/covers/fixture.jpg">`)
		case "/covers/fixture.jpg":
			_, _ = w.Write(append([]byte{0xff, 0xd8}, make([]byte, 64)...))
		default:
			t.Errorf("unexpected cover URL %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	configPath := isolatedCLIConfig(t)
	legacyPath := filepath.Join(filepath.Dir(configPath), "subscriptions.json")
	legacy := fmt.Sprintf(`{"subscriptions":[{"slug":"renamed","series_url":%q},{"slug":"remote-only","series_url":%q}]}`, server.URL+"/manga/wrong/", server.URL+"/manga/remote-only/")
	if err := storeWrite(t, legacyPath, legacy); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"renamed", "fallback"} {
		writeImage(t, filepath.Join(root, slug, "chapter-001"), "001.jpg", validJPEG)
	}
	writeSeriesManifestFixture(t, filepath.Join(root, "renamed"), server.URL+"/manga/original/")
	var stdout, stderr bytes.Buffer
	deps := Dependencies{HTTP: server.Client(), BaseURL: server.URL, ConfigPath: configPath}
	if code := Main(context.Background(), []string{"cover", "--all", "--out", root, "--json"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("cover code=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	var reports []CoverReport
	if err := json.Unmarshal(stdout.Bytes(), &reports); err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 || reports[0].Slug != "fallback" || reports[1].Slug != "renamed" {
		t.Fatalf("reports=%+v", reports)
	}
	for _, report := range reports {
		if report.Error != "" || report.Cover != filepath.Join(root, report.Slug, "cover.jpg") {
			t.Fatalf("report=%+v", report)
		}
	}
	data, err := os.ReadFile(legacyPath)
	if err != nil || string(data) != legacy {
		t.Fatalf("legacy file changed: %q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, "original")); !os.IsNotExist(err) {
		t.Fatalf("cover created a new directory from URL slug: %v", err)
	}
}

func TestCoverCommandRejectsMixedTargets(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"cover", "--all", "https://komiku.org/manga/x/"}, &stdout, &stderr, Dependencies{})
	if code == 0 {
		t.Fatal("mixed --all and URL was accepted")
	}
}

func TestLibraryReportIncludesCoverPath(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "fixture")
	if err := os.MkdirAll(filepath.Join(dir, "chapter-001"), 0o755); err != nil {
		t.Fatal(err)
	}
	cover := filepath.Join(dir, "cover.jpg")
	if err := os.WriteFile(cover, []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := scanLibrary(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Series) != 1 || report.Series[0].Cover != cover {
		t.Fatalf("cover not reported: %+v", report.Series)
	}
}

func TestInfoReportIncludesCoverPath(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "fixture")
	if err := os.MkdirAll(filepath.Join(dir, "chapter-001"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cover.webp"), []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := localSeriesInfo(root, "fixture", "https://komiku.org/manga/fixture/", false)
	if err != nil {
		t.Fatal(err)
	}
	if info.Cover != filepath.Join(dir, "cover.webp") {
		t.Fatalf("cover = %q", info.Cover)
	}
}
