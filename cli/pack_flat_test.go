package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func offlineHTTPClient(t *testing.T) *http.Client {
	t.Helper()
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("flat pack must stay offline")
	})}
	t.Cleanup(func() {
		if calls != 0 {
			t.Fatalf("flat pack made %d HTTP requests", calls)
		}
	})
	return client
}

func newFlatSeries(t *testing.T, numbers ...int) string {
	t.Helper()
	seriesDir := filepath.Join(t.TempDir(), "flat-series")
	for _, number := range numbers {
		writeManifestTestImage(t, filepath.Join(seriesDir, fmt.Sprintf("chapter-%03d", number)), "001.jpg")
	}
	return seriesDir
}

func TestFlatPackPacksContiguousChaptersOfflineWithoutManifest(t *testing.T) {
	seriesDir := newFlatSeries(t, 1, 2)
	client := offlineHTTPClient(t)
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"pack", seriesDir, "--flat", "--series", "Sakamoto Days", "--preset", "raw"}, &stdout, &stderr, Dependencies{HTTP: client})
	if code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	archive := filepath.Join(seriesDir, "Sakamoto Days Chapters 1-2.cbz")
	if _, err := os.Stat(archive); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(PackManifestPath(seriesDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("flat pack wrote a manifest: %v", err)
	}
	reader, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	entries := make([]string, 0, len(reader.File))
	for _, file := range reader.File {
		entries = append(entries, file.Name)
	}
	want := []string{"Chapter 001/001.jpg", "Chapter 002/001.jpg"}
	if strings.Join(entries, ",") != strings.Join(want, ",") {
		t.Fatalf("archive entries=%v, want %v", entries, want)
	}
}

func TestFlatPackDefaultsSeriesNameToDirectory(t *testing.T) {
	seriesDir := newFlatSeries(t, 5)
	client := offlineHTTPClient(t)
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"pack", seriesDir, "--flat", "--preset", "raw"}, &stdout, &stderr, Dependencies{HTTP: client})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(seriesDir, "flat-series Chapter 5.cbz")); err != nil {
		t.Fatal(err)
	}
}

func TestFlatPackRejectsBrokenLocalLayouts(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T) string
		args    []string
		want    string
	}{
		{
			name:    "chapter gap",
			prepare: func(t *testing.T) string { return newFlatSeries(t, 1, 3) },
			args:    []string{"--flat"},
			want:    "not contiguous",
		},
		{
			name: "decimal chapter",
			prepare: func(t *testing.T) string {
				dir := newFlatSeries(t, 1)
				writeManifestTestImage(t, filepath.Join(dir, "chapter-001.5"), "001.jpg")
				return dir
			},
			args: []string{"--flat"},
			want: "requires integer chapters",
		},
		{
			name: "sparse pages",
			prepare: func(t *testing.T) string {
				dir := newFlatSeries(t, 1)
				writeManifestTestImage(t, filepath.Join(dir, "chapter-001"), "003.jpg")
				return dir
			},
			args: []string{"--flat"},
			want: "missing page 002",
		},
		{
			name:    "flag without flat",
			prepare: func(t *testing.T) string { return newFlatSeries(t, 1) },
			args:    []string{"--series", "X"},
			want:    "--series requires --flat",
		},
		{
			name:    "flat with vol",
			prepare: func(t *testing.T) string { return newFlatSeries(t, 1) },
			args:    []string{"--flat", "--vol", "1"},
			want:    "--vol requires a volume mapping",
		},
		{
			name:    "flat with recovery",
			prepare: func(t *testing.T) string { return newFlatSeries(t, 1) },
			args:    []string{"--flat", "--recover-wikipedia"},
			want:    "mutually exclusive",
		},
		{
			name: "existing manifest",
			prepare: func(t *testing.T) string {
				dir := newFlatSeries(t, 1)
				if err := os.WriteFile(filepath.Join(dir, ".pack.json"), []byte(`{"version":1}`), 0o644); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			args: []string{"--flat"},
			want: "pack manifest already exists",
		},
		{
			name: "pre-existing archive",
			prepare: func(t *testing.T) string {
				dir := newFlatSeries(t, 1)
				if err := os.WriteFile(filepath.Join(dir, "flat-series Chapter 1.cbz"), []byte("keep"), 0o644); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			args: []string{"--flat"},
			want: "pre-existing archive",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			seriesDir := test.prepare(t)
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("flat pack must stay offline")
			})}
			args := append([]string{"pack", seriesDir, "--preset", "raw"}, test.args...)
			var stderr bytes.Buffer
			code := Main(context.Background(), args, io.Discard, &stderr, Dependencies{HTTP: client})
			if code == 0 || !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("code=%d stderr=%q, want %q", code, stderr.String(), test.want)
			}
			archives, err := filepath.Glob(filepath.Join(seriesDir, "*.cbz"))
			if err != nil {
				t.Fatal(err)
			}
			if test.name == "pre-existing archive" {
				if len(archives) != 1 {
					t.Fatalf("pre-existing archive was removed: %#v", archives)
				}
				return
			}
			if len(archives) != 0 {
				t.Fatalf("failed flat pack wrote archives: %#v", archives)
			}
		})
	}
}
