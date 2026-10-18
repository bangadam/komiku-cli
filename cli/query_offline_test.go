package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestInfoOfflineReadsChaptersAndVolumesWithoutHTTP(t *testing.T) {
	root := t.TempDir()
	seriesDir := filepath.Join(root, "sakamoto-days")
	for number := 1; number <= 3; number += 1 {
		writeManifestTestImage(t, filepath.Join(seriesDir, "chapter-00"+string(rune('0'+number))), "001.jpg")
	}
	if err := os.WriteFile(filepath.Join(seriesDir, ".state.json"), []byte(`{"done":[1,2]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := `{"source":"https://api.mangadex.org/x","volumes":[{"volume":1,"start":1,"end":3}]}`
	if err := os.WriteFile(filepath.Join(seriesDir, ".volumes.json"), []byte(cache), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{
		"info", "https://komiku.org/manga/sakamoto-days/", "--offline", "--volumes", "--out", root, "--json",
	}, &stdout, &stderr, Dependencies{HTTP: offlineHTTPClient(t)})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var info SeriesInfo
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if len(info.Chapters) != 3 || info.DoneCount != 2 {
		t.Fatalf("chapters=%d done=%d", len(info.Chapters), info.DoneCount)
	}
	if len(info.Volumes) != 1 || info.Volumes[0].End != 3 {
		t.Fatalf("volumes=%+v", info.Volumes)
	}
	if info.VolumeError != "" {
		t.Fatalf("unexpected volume error: %q", info.VolumeError)
	}
}

func TestInfoOfflineReportsMissingMappingWithoutFailing(t *testing.T) {
	root := t.TempDir()
	seriesDir := filepath.Join(root, "flat-series")
	writeManifestTestImage(t, filepath.Join(seriesDir, "chapter-001"), "001.jpg")

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{
		"info", "https://komiku.org/manga/flat-series/", "--offline", "--volumes", "--out", root, "--json",
	}, &stdout, &stderr, Dependencies{HTTP: offlineHTTPClient(t)})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var info SeriesInfo
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if len(info.Chapters) != 1 || len(info.Volumes) != 0 || info.VolumeError == "" {
		t.Fatalf("info=%+v", info)
	}
}

func TestInfoOfflineFallsBackToManifestMapping(t *testing.T) {
	root := t.TempDir()
	seriesDir := filepath.Join(root, "mapped-series")
	source := "chapter-001"
	writeManifestTestImage(t, filepath.Join(seriesDir, source), "001.jpg")
	if err := os.WriteFile(PackManifestPath(seriesDir), []byte(`{"version":1,"series":"mapped-series","mappings":[{"volume":7,"start":1,"end":1,"provenance":"download-mapping"}],"chapters":[{"identity_provenance":"recovered-local","display":"1","number":1,"volume":7,"source_dir":"chapter-001","expected_pages":1,"completed":true}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{
		"info", "https://komiku.org/manga/mapped-series/", "--offline", "--volumes", "--out", root, "--json",
	}, &stdout, &stderr, Dependencies{HTTP: offlineHTTPClient(t)})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var info SeriesInfo
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if len(info.Volumes) != 1 || info.Volumes[0].Volume != 7 {
		t.Fatalf("volumes=%+v", info.Volumes)
	}
}
