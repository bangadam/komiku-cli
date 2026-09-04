package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// validJPEG is enough magic for store.ValidResumeFile, which also requires
// MinResumeSize bytes on disk, so we pad to 11 KiB.
var validJPEG = append([]byte{0xff, 0xd8}, make([]byte, 11*1024)...)

func writeImage(t *testing.T, dir, name string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyDetectsBrokenPagesAndMissingGaps(t *testing.T) {
	root := t.TempDir()
	series := filepath.Join(root, "fixture")
	// Chapter 1: two valid pages, healthy.
	writeImage(t, filepath.Join(series, "chapter-001"), "001.jpg", validJPEG)
	writeImage(t, filepath.Join(series, "chapter-001"), "002.jpg", validJPEG)
	// Chapter 2: one valid page, one corrupt file saved as image -> broken.
	writeImage(t, filepath.Join(series, "chapter-002"), "001.jpg", validJPEG)
	writeImage(t, filepath.Join(series, "chapter-002"), "002.jpg", []byte("<html>broken</html>"))
	// Chapter 3: valid pages 1 and 3, no page 2 file -> missing gap.
	writeImage(t, filepath.Join(series, "chapter-003"), "001.jpg", validJPEG)
	writeImage(t, filepath.Join(series, "chapter-003"), "003.jpg", validJPEG)

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"verify", series}, &stdout, &stderr, Dependencies{})
	if code != 0 {
		t.Fatalf("verify code=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "OK chapter-001 pages=2/2") {
		t.Fatalf("healthy chapter not reported: %s", out)
	}
	if !strings.Contains(out, "BROKEN chapter-002 pages=1/2") || !strings.Contains(out, "broken: 002.jpg") {
		t.Fatalf("broken page not flagged: %s", out)
	}
	if !strings.Contains(out, "BROKEN chapter-003 pages=2/2") || !strings.Contains(out, "missing: page 002") {
		t.Fatalf("missing gap not flagged: %s", out)
	}
	if strings.Contains(out, "all chapters verified") {
		t.Fatalf("should not claim all verified when problems exist: %s", out)
	}
}

func TestVerifyHealthySeriesPrintsFooter(t *testing.T) {
	root := t.TempDir()
	series := filepath.Join(root, "fixture")
	writeImage(t, filepath.Join(series, "chapter-001"), "001.jpg", validJPEG)
	writeImage(t, filepath.Join(series, "chapter-001"), "002.jpg", validJPEG)

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"verify", series}, &stdout, &stderr, Dependencies{})
	if code != 0 {
		t.Fatalf("verify code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "all chapters verified") {
		t.Fatalf("missing success footer: %s", stdout.String())
	}
}

func TestVerifyJSONReport(t *testing.T) {
	root := t.TempDir()
	series := filepath.Join(root, "fixture")
	writeImage(t, filepath.Join(series, "vol-01", "chapter-001"), "001.jpg", validJPEG)
	writeImage(t, filepath.Join(series, "vol-01", "chapter-002"), "001.jpg", []byte("corrupt"))

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"verify", series, "--json"}, &stdout, &stderr, Dependencies{})
	if code != 0 {
		t.Fatalf("verify json code=%d stderr=%s", code, stderr.String())
	}
	var report VerifyReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout.String())
	}
	if len(report.Chapters) != 2 {
		t.Fatalf("expected 2 chapters, got %d", len(report.Chapters))
	}
	if report.Chapters[0].ValidPages != 1 || report.Chapters[1].BrokenPages[0] != "001.jpg" {
		t.Fatalf("report=%#v", report)
	}
	if report.Healthy {
		t.Fatalf("report should not be healthy: %#v", report)
	}
	if report.Chapters[0].Dir != "chapter-001" {
		t.Fatalf("dir field=%q", report.Chapters[0].Dir)
	}
}

func TestVerifyRejectsMissingDirectory(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"verify", filepath.Join(t.TempDir(), "nope")}, &stdout, &stderr, Dependencies{})
	if code == 0 {
		t.Fatalf("verify missing dir should fail, stdout=%s", stdout.String())
	}
}

func TestDownloadLatestSelector(t *testing.T) {
	var imageHits int
	server := chapterListServer(t, &imageHits)
	defer server.Close()
	root := t.TempDir()
	configPath := filepath.Join(root, "missing-config.json")
	dependencies := Dependencies{HTTP: server.Client(), ConfigPath: configPath, Now: func() time.Time { return time.Unix(3, 0) }}

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"dl", server.URL + "/manga/fixture/", "--ch", "latest:1", "--no-tui", "--json", "--out", root, "--delay", "0s"}, &stdout, &stderr, dependencies)
	if code != 0 {
		t.Fatalf("latest code=%d stderr=%s", code, stderr.String())
	}
	var report DownloadReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout.String())
	}
	if report.Requested != 1 || report.Results[0].Chapter != "3" {
		t.Fatalf("latest should pick only chapter 3: %#v", report)
	}
}

func TestDownloadLatestSelectorRejectsBadCount(t *testing.T) {
	server := chapterListServer(t, new(int))
	defer server.Close()
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"dl", server.URL + "/manga/fixture/", "--ch", "latest:abc", "--no-tui", "--out", root, "--delay", "0s"}, &stdout, &stderr, Dependencies{HTTP: server.Client(), ConfigPath: filepath.Join(root, "c.json")})
	if code == 0 {
		t.Fatalf("latest:abc should fail, stdout=%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "invalid latest selector") {
		t.Fatalf("expected validation error, got: %s", stderr.String())
	}
}
