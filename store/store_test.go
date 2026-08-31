package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestValidResumeFileRequiresMoreThan10KBAndMagic(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, size int, header []byte) string {
		t.Helper()
		data := make([]byte, size)
		copy(data, header)
		filename := filepath.Join(dir, name)
		if err := os.WriteFile(filename, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return filename
	}
	if ValidResumeFile(write("exact.jpg", 10*1024, []byte{0xff, 0xd8})) {
		t.Fatal("exactly 10KB must not be skipped")
	}
	if ValidResumeFile(write("large.html", 11*1024, []byte("<html>"))) {
		t.Fatal("invalid large payload must not be skipped")
	}
	if !ValidResumeFile(write("large.jpg", 10*1024+1, []byte{0xff, 0xd8})) {
		t.Fatal("valid image larger than 10KB must be skipped")
	}
}

func TestMaterializeDoneAcceptsValidSmallImage(t *testing.T) {
	series, err := Open(t.TempDir(), "series")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := series.ChapterDir("1", "", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 1800)
	copy(data, []byte{0xff, 0xd8})
	if err := os.WriteFile(filepath.Join(dir, "001.jpg"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if ValidResumeFile(filepath.Join(dir, "001.jpg")) {
		t.Fatal("small image must remain ineligible for per-file gap resume")
	}
	materialized, err := series.MaterializeDone("1", "", 0, true)
	if err != nil || !materialized {
		t.Fatalf("state-confirmed DONE chapter was not materialized: %v", err)
	}
}

func TestMarkDoneAtomicFailureLeavesOldStateReadable(t *testing.T) {
	dir := t.TempDir()
	series, err := Open(dir, "series")
	if err != nil {
		t.Fatal(err)
	}
	if err := series.MarkDone(1); err != nil {
		t.Fatal(err)
	}
	originalRename := atomicRename
	atomicRename = func(_, _ string) error { return errors.New("injected rename failure") }
	t.Cleanup(func() { atomicRename = originalRename })
	if err := series.MarkDone(2); err == nil {
		t.Fatal("expected atomic rename failure")
	}
	data, err := os.ReadFile(filepath.Join(series.Dir(), ".state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("old state corrupted: %v", err)
	}
	if len(state.Done) != 1 || state.Done[0] != 1 {
		t.Fatalf("state changed after failed atomic write: %#v", state)
	}
}

func TestChapterDirectoriesFlatAndVolume(t *testing.T) {
	series, err := Open(t.TempDir(), "series")
	if err != nil {
		t.Fatal(err)
	}
	flat, err := series.ChapterDir("271.5", "", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(flat) != "chapter-271.5" || filepath.Base(filepath.Dir(flat)) != "series" {
		t.Fatalf("flat directory = %s", flat)
	}
	volume, err := series.ChapterDir("1", "", 17, false)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(volume) != "chapter-001" || filepath.Base(filepath.Dir(volume)) != "vol-17" {
		t.Fatalf("volume directory = %s", volume)
	}
}

func TestCountChapterPagesAcceptsSmallMagicValidFiles(t *testing.T) {
	dir := t.TempDir()
	for page, header := range [][]byte{{0xff, 0xd8}, []byte("\x89PNG\r\n\x1a\n")} {
		data := make([]byte, 1800)
		copy(data, header)
		filename := filepath.Join(dir, fmt.Sprintf("%03d.jpg", page+1))
		if err := os.WriteFile(filename, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pages, err := CountChapterPages(dir)
	if err != nil || pages != 2 {
		t.Fatalf("pages=%d err=%v", pages, err)
	}
}
