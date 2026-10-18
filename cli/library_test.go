package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bangadam/komiku-cli/store"
)

func writeCBZ(t *testing.T, path string, entries map[string][]byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	w := zip.NewWriter(out)
	for name, body := range entries {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLibraryDashboardScansSeriesHealth(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	deps := Dependencies{ConfigPath: configPath}

	// Healthy series: 2 chapters, all valid.
	writeImage(t, filepath.Join(root, "frieren", "chapter-001"), "001.jpg", validJPEG)
	writeImage(t, filepath.Join(root, "frieren", "chapter-001"), "002.jpg", validJPEG)
	writeImage(t, filepath.Join(root, "frieren", "chapter-002"), "001.jpg", validJPEG)
	// Broken series: one corrupt page in chapter-001, mapped layout + CBZ.
	writeImage(t, filepath.Join(root, "jjk", "vol-01", "chapter-001"), "001.jpg", validJPEG)
	writeImage(t, filepath.Join(root, "jjk", "vol-01", "chapter-001"), "002.jpg", []byte("<html>corrupt</html>"))
	writeCBZ(t, filepath.Join(root, "jjk", "Jujutsu Kaisen Volume 01.cbz"), map[string][]byte{
		"Chapter 001/001.jpg": validJPEG,
	})
	// A state file marks one chapter done.
	if err := storeWrite(t, filepath.Join(root, "jjk", ".state.json"), `{"done":[1]}`); err != nil {
		t.Fatal(err)
	}
	// A non-series directory must be skipped.
	if err := os.MkdirAll(filepath.Join(root, "random-folder"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"library", "--out", root}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("library code=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !bytes.Contains([]byte(out), []byte("series=2")) {
		t.Fatalf("expected 2 series, got: %s", out)
	}
	if !bytes.Contains([]byte(out), []byte("frieren")) {
		t.Fatalf("frieren not shown: %s", out)
	}
	if bytes.Contains([]byte(out), []byte("subscri")) {
		t.Fatalf("removed subscription marker shown: %s", out)
	}
	if !bytes.Contains([]byte(out), []byte("jjk")) {
		t.Fatalf("jjk not shown: %s", out)
	}
	if !bytes.Contains([]byte(out), []byte("BROKEN")) {
		t.Fatalf("broken series not flagged: %s", out)
	}
	if !bytes.Contains([]byte(out), []byte("problems=1")) {
		t.Fatalf("problem count not in header: %s", out)
	}
}

func TestLibraryJSONReport(t *testing.T) {
	root := t.TempDir()
	deps := Dependencies{ConfigPath: filepath.Join(root, "config.json")}

	writeImage(t, filepath.Join(root, "frieren", "chapter-001"), "001.jpg", validJPEG)
	writeCBZ(t, filepath.Join(root, "frieren", "Frieren Volume 01.cbz"), map[string][]byte{
		"Chapter 001/001.jpg": validJPEG,
	})

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"library", "--out", root, "--json"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("library json code=%d stderr=%s", code, stderr.String())
	}
	var report LibraryReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout.String())
	}
	if len(report.Series) != 1 {
		t.Fatalf("expected 1 series, got %d", len(report.Series))
	}
	s := report.Series[0]
	if s.Slug != "frieren" || s.Chapters != 1 || s.CBZ != 1 || !s.Healthy {
		t.Fatalf("series=%#v", s)
	}
	if report.TotalCBZ != 1 || report.TotalBytes <= 0 {
		t.Fatalf("report totals=%#v", report)
	}
	var raw struct {
		Series []map[string]json.RawMessage `json:"series"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, series := range raw.Series {
		for _, key := range []string{"subscribed", "subscriptions", "subscription"} {
			if _, exists := series[key]; exists {
				t.Fatalf("removed field %q is present: %s", key, stdout.String())
			}
		}
	}
}

func TestLibraryEmptyRootIsHelpful(t *testing.T) {
	root := t.TempDir()
	deps := Dependencies{ConfigPath: filepath.Join(root, "config.json")}

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"library", "--out", root}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("library empty code=%d stderr=%s", code, stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte("no series downloaded yet")) {
		t.Fatalf("expected placeholder, got: %s", stdout.String())
	}
}

func writeSeriesManifestFixture(t *testing.T, dir, seriesURL string) {
	t.Helper()
	manifest := PackManifest{
		Version:   PackManifestVersion,
		Series:    filepath.Base(dir),
		SeriesURL: seriesURL,
		Mappings:  []PackManifestMapping{{Volume: 1, Start: 1, End: 3, Provenance: "download-mapping"}},
	}
	if err := store.WriteJSONAtomic(PackManifestPath(dir), manifest); err != nil {
		t.Fatal(err)
	}
}

func TestLocalSeriesURLPrefersManifestThenSlugFallback(t *testing.T) {
	for _, test := range []struct {
		name        string
		slug        string
		manifestURL string
		base        string
		want        string
	}{
		{name: "manifest", slug: "renamed", manifestURL: "https://komiku.org/manga/original/", base: "http://127.0.0.1:9999", want: "https://komiku.org/manga/original/"},
		{name: "fallback", slug: "fixture", base: "http://127.0.0.1:9999/", want: "http://127.0.0.1:9999/manga/fixture/"},
		{name: "default host", slug: "fixture", want: "https://komiku.org/manga/fixture/"},
		{name: "invalid manifest URL", slug: "fixture", manifestURL: "https://example.invalid/manga/wrong/", base: "http://127.0.0.1:9999", want: "http://127.0.0.1:9999/manga/fixture/"},
		{name: "escaped slug", slug: "fixture#extra", base: "http://127.0.0.1:9999", want: "http://127.0.0.1:9999/manga/fixture%23extra/"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, test.slug)
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if test.manifestURL != "" {
				writeSeriesManifestFixture(t, dir, test.manifestURL)
			}
			before := snapshotUpdateFiles(t, root)
			got, err := localSeriesURL(root, test.slug, test.base)
			if err != nil || got != test.want {
				t.Fatalf("URL=%q want=%q err=%v", got, test.want, err)
			}
			if !reflect.DeepEqual(before, snapshotUpdateFiles(t, root)) {
				t.Fatal("resolving URL changed library files")
			}
		})
	}
}

func TestLocalSeriesTargetsSkipInvalidSlugWithoutBlockingLibrary(t *testing.T) {
	root := t.TempDir()
	for _, slug := range []string{" invalid", "fixture"} {
		if err := storeWrite(t, filepath.Join(root, slug, ".state.json"), `{"done":[1]}`); err != nil {
			t.Fatal(err)
		}
	}
	targets, err := localSeriesTargets(root, Dependencies{})
	if err != nil || len(targets) != 1 || targets[0].Slug != "fixture" {
		t.Fatalf("targets=%+v err=%v", targets, err)
	}
}

func TestSeriesHasDownloadsRecognizesLocalStorage(t *testing.T) {
	root := t.TempDir()
	writeImage(t, filepath.Join(root, "flat", "chapter-001"), "001.jpg", validJPEG)
	writeImage(t, filepath.Join(root, "mapped", "vol-01", "chapter-001"), "001.jpg", validJPEG)
	if err := storeWrite(t, filepath.Join(root, "state", ".state.json"), `{"done":[1]}`); err != nil {
		t.Fatal(err)
	}
	writeCBZ(t, filepath.Join(root, "archive", "Volume 01.CBZ"), map[string][]byte{"Chapter 001/001.jpg": validJPEG})
	for _, slug := range []string{"flat", "mapped", "state", "archive"} {
		if !seriesHasDownloads(filepath.Join(root, slug)) {
			t.Errorf("%s not recognized as a local download", slug)
		}
	}
}

func TestLibraryIgnoresLegacySubscriptionFile(t *testing.T) {
	root := t.TempDir()
	configPath := isolatedCLIConfig(t)
	legacyPath := filepath.Join(filepath.Dir(configPath), "subscriptions.json")
	legacy := `{"subscriptions":[{"slug":"remote-only","series_url":"https://komiku.org/manga/remote-only/"}]}`
	if err := storeWrite(t, legacyPath, legacy); err != nil {
		t.Fatal(err)
	}
	writeImage(t, filepath.Join(root, "fixture", "chapter-001"), "001.jpg", validJPEG)
	var stdout, stderr bytes.Buffer
	if code := Main(context.Background(), []string{"library", "--out", root, "--json"}, &stdout, &stderr, Dependencies{}); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var report LibraryReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Series) != 1 || report.Series[0].Slug != "fixture" || bytes.Contains(stdout.Bytes(), []byte("subscri")) {
		t.Fatalf("report=%s", stdout.String())
	}
	data, err := os.ReadFile(legacyPath)
	if err != nil || string(data) != legacy {
		t.Fatalf("legacy file changed: %q err=%v", data, err)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		512:     "512B",
		2048:    "2.0K",
		1048576: "1.0M",
	}
	for input, want := range cases {
		if got := humanBytes(input); got != want {
			t.Errorf("humanBytes(%d)=%q, want %q", input, got, want)
		}
	}
}

func storeWrite(t *testing.T, path, content string) error {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
