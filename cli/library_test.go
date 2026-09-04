package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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

func TestLibraryDashboardScansSeriesHealthAndSubscriptions(t *testing.T) {
	root := t.TempDir()
	subsPath := filepath.Join(root, "subs.json")
	configPath := filepath.Join(root, "config.json")
	deps := Dependencies{ConfigPath: configPath}.WithSubscriptionsPath(subsPath)

	// Healthy series: 2 chapters, all valid, subscribed.
	writeImage(t, filepath.Join(root, "frieren", "chapter-001"), "001.jpg", validJPEG)
	writeImage(t, filepath.Join(root, "frieren", "chapter-001"), "002.jpg", validJPEG)
	writeImage(t, filepath.Join(root, "frieren", "chapter-002"), "001.jpg", validJPEG)
	if err := SaveSubscriptions(subsPath, SubscriptionsFile{Subscriptions: []Subscription{{Slug: "frieren", SeriesURL: "https://komiku.org/manga/frieren/"}}}); err != nil {
		t.Fatal(err)
	}
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
	if !bytes.Contains([]byte(out), []byte("frieren")) || !bytes.Contains([]byte(out), []byte("[subscribed]")) {
		t.Fatalf("frieren not shown or subscription marker missing: %s", out)
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
	deps := Dependencies{ConfigPath: filepath.Join(root, "config.json")}.WithSubscriptionsPath(filepath.Join(root, "subs.json"))

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
}

func TestLibraryEmptyRootIsHelpful(t *testing.T) {
	root := t.TempDir()
	deps := Dependencies{ConfigPath: filepath.Join(root, "config.json")}.WithSubscriptionsPath(filepath.Join(root, "subs.json"))

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"library", "--out", root}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("library empty code=%d stderr=%s", code, stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte("no series downloaded yet")) {
		t.Fatalf("expected placeholder, got: %s", stdout.String())
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
