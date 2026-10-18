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
	"strings"
	"testing"
)

func TestParseVolumeMap(t *testing.T) {
	mappings, err := ParseVolumeMap("2:6-14, 1:1-5,23:201-201")
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ volume, start, end int }{{1, 1, 5}, {2, 6, 14}, {23, 201, 201}}
	if len(mappings) != len(want) {
		t.Fatalf("mappings=%+v", mappings)
	}
	for index, expected := range want {
		got := mappings[index]
		if got.Volume != expected.volume || got.Start != expected.start || got.End != expected.end {
			t.Fatalf("mapping[%d]=%+v, want %+v", index, got, expected)
		}
	}

	for _, invalid := range []string{"", "1", "1:5", "0:1-5", "1:5-1", "1:1-5,2:5-9", "1:1-5,1:6-9"} {
		if _, err := ParseVolumeMap(invalid); err == nil {
			t.Fatalf("ParseVolumeMap(%q) accepted an invalid mapping", invalid)
		}
	}
}

func TestPackWithVolumeMapStaysOfflineAndWritesManifest(t *testing.T) {
	seriesDir := newLegacySeries(t, []float64{1, 2, 3, 4, 5}, []float64{1, 2, 3, 4, 5})
	client := offlineHTTPClient(t)

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{
		"pack", seriesDir, "--volume-map", "1:1-5", "--vol", "1", "--preset", "raw",
	}, &stdout, &stderr, Dependencies{HTTP: client})
	if code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(seriesDir, "sakamoto-days Volume 01.cbz")); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadPackManifest(seriesDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Mappings) != 1 || manifest.Mappings[0].Provenance != "download-mapping" {
		t.Fatalf("manifest mappings=%+v", manifest.Mappings)
	}
	if len(manifest.Chapters) != 5 {
		t.Fatalf("manifest chapters=%+v", manifest.Chapters)
	}
	if !strings.Contains(stdout.String(), "volume mapping: 1:1-5") {
		t.Fatalf("mapping was not reported: %q", stdout.String())
	}
}

func TestPackWithVolumeMapReadsVolumeScopedLayout(t *testing.T) {
	seriesDir := filepath.Join(t.TempDir(), "mapped-series")
	for number := 1; number <= 2; number += 1 {
		writeManifestTestImage(t, filepath.Join(seriesDir, "vol-01", fmt.Sprintf("chapter-%03d", number)), "001.jpg")
	}
	if err := os.WriteFile(filepath.Join(seriesDir, ".state.json"), []byte(`{"done":[1,2]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	client := offlineHTTPClient(t)

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{
		"pack", seriesDir, "--volume-map", "1:1-2", "--preset", "raw",
	}, &stdout, &stderr, Dependencies{HTTP: client})
	if code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	manifest, err := LoadPackManifest(seriesDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, chapter := range manifest.Chapters {
		if !strings.HasPrefix(chapter.SourceDir, "vol-01/") {
			t.Fatalf("source dir was not preserved: %+v", chapter)
		}
	}
}

func TestPackWithVolumeMapRejectsIncompleteVolume(t *testing.T) {
	seriesDir := newLegacySeries(t, []float64{1, 2}, []float64{1, 2})
	client := offlineHTTPClient(t)

	var stderr bytes.Buffer
	code := Main(context.Background(), []string{
		"pack", seriesDir, "--volume-map", "1:1-5", "--preset", "raw",
	}, io.Discard, &stderr, Dependencies{HTTP: client})
	if code == 0 || !strings.Contains(stderr.String(), "missing") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(PackManifestPath(seriesDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed pack wrote a manifest: %v", err)
	}
}

func TestPackWithVolumeMapRejectsConflictingFlags(t *testing.T) {
	seriesDir := newLegacySeries(t, []float64{1}, []float64{1})
	tests := [][]string{
		{"--flat", "--volume-map", "1:1-1"},
		{"--recover-wikipedia", "--volume-map", "1:1-1"},
	}
	for _, flags := range tests {
		args := append([]string{"pack", seriesDir, "--preset", "raw"}, flags...)
		var stderr bytes.Buffer
		code := Main(context.Background(), args, io.Discard, &stderr, Dependencies{HTTP: &http.Client{}})
		if code == 0 {
			t.Fatalf("flags %v were accepted", flags)
		}
	}
}

func TestPackWithVolUsesLocalMappingOffline(t *testing.T) {
	seriesDir := newLegacySeries(t, []float64{1, 2, 3}, []float64{1, 2, 3})
	cache := `{"source":"https://api.mangadex.org/x","volumes":[{"volume":1,"start":1,"end":3}]}`
	if err := os.WriteFile(filepath.Join(seriesDir, ".volumes.json"), []byte(cache), 0o644); err != nil {
		t.Fatal(err)
	}
	client := offlineHTTPClient(t)

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"pack", seriesDir, "--vol", "1", "--preset", "raw"}, &stdout, &stderr, Dependencies{HTTP: client})
	if code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "using the local volume mapping") {
		t.Fatalf("offline mapping note missing: %q", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(seriesDir, "sakamoto-days Volume 01.cbz")); err != nil {
		t.Fatal(err)
	}
}
