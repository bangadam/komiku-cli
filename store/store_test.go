package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func writeResumeImage(t *testing.T, path string, size int) {
	t.Helper()
	data := make([]byte, size)
	copy(data, []byte{0xff, 0xd8, 0xff, 0xe0})
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStateSurvivesReopen(t *testing.T) {
	root := t.TempDir()
	series, err := Open(root, "series")
	if err != nil {
		t.Fatal(err)
	}
	if series.IsDone(1) {
		t.Fatal("fresh store reports chapter 1 as done")
	}
	if err := series.MarkDone(1); err != nil {
		t.Fatal(err)
	}
	if !series.IsDone(1) {
		t.Fatal("chapter 1 not done after MarkDone")
	}
	if err := series.MarkDone(1); err != nil {
		t.Fatalf("repeated MarkDone: %v", err)
	}
	reopened, err := Open(root, "series")
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.IsDone(1) {
		t.Fatal("completed chapter lost during loadState")
	}
	if reopened.IsDone(2) {
		t.Fatal("unrelated chapter reported done")
	}
}

func TestReadDoneMissingStateAndInvalidArguments(t *testing.T) {
	done, err := ReadDone(t.TempDir(), "absent")
	if err != nil || done != nil {
		t.Fatalf("missing state = %#v, %v", done, err)
	}
	if _, err := ReadDone("", "series"); err == nil || !strings.Contains(err.Error(), "output root is empty") {
		t.Fatalf("empty root err = %v", err)
	}
	for _, name := range []string{"", ".", "..", "a/b", `a\b`, "a\x00b"} {
		if _, err := ReadDone(t.TempDir(), name); err == nil {
			t.Fatalf("series %q accepted", name)
		}
	}
}

func TestReadDoneDecodesCorruptState(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "series")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".state.json"), []byte(`{"done":"not-numbers"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDone(root, "series"); err == nil || !strings.Contains(err.Error(), "decode state") {
		t.Fatalf("corrupt state err = %v", err)
	}
}

func TestReadDoneSortsCompletedChapters(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "series")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".state.json")
	if err := os.WriteFile(path, []byte(`{"done":[3,1.5,2]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	done, err := ReadDone(root, "series")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(done, []float64{1.5, 2, 3}) {
		t.Fatalf("done = %#v", done)
	}
}

func TestExistingPageRequiresValidResumeFile(t *testing.T) {
	if name, ok := ExistingPage(filepath.Join(t.TempDir(), "missing"), 1); ok || name != "" {
		t.Fatalf("missing directory = %q, %v", name, ok)
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "001.jpg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := ExistingPage(dir, 1); ok {
		t.Fatal("directory named like a page was treated as a resume file")
	}
	writeResumeImage(t, filepath.Join(dir, "002.jpg"), 512)
	if _, ok := ExistingPage(dir, 2); ok {
		t.Fatal("too-small page was treated as resumable")
	}
	if err := os.WriteFile(filepath.Join(dir, "003.jpg"), bytes.Repeat([]byte("<html>"), 2048), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := ExistingPage(dir, 3); ok {
		t.Fatal("non-image payload was treated as resumable")
	}
	valid := filepath.Join(dir, "004.jpg")
	writeResumeImage(t, valid, int(MinResumeSize)+1)
	name, ok := ExistingPage(dir, 4)
	if !ok || name != valid || !strings.HasPrefix(filepath.Base(name), "004.") {
		t.Fatalf("valid page = %q, %v", name, ok)
	}
}

func TestWritePageRejectsUnsafeExtensions(t *testing.T) {
	dir := t.TempDir()
	for _, extension := range []string{"", "jpg", "jp/g", `jp\g`, "jp\x00g"} {
		if err := WritePage(dir, 1, extension, strings.NewReader("body")); err == nil {
			t.Fatalf("extension %q accepted", extension)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("rejected writes created files: %v", entries)
	}
}

func TestWritePageReplacesStalePageFiles(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "005.png")
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46}
	if err := WritePage(dir, 5, ".jpg", bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "005.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("page body = %v, want %v", got, body)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale extension survived: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".part") {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}
	if len(entries) != 1 || entries[0].Name() != "005.jpg" {
		t.Fatalf("directory contents = %v", entries)
	}
}

func TestWriteJSONAtomicCreatesParentsAndRoundTrips(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "nested", "deeper", "state.json")
	want := State{Done: []float64{1, 2.5}}
	if err := WriteJSONAtomic(filename, want); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatalf("payload missing trailing newline: %q", data)
	}
	var got State
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
	info, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(filename))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

func TestOpenRejectsCorruptPersistedState(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "series")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".state.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root, "series"); err == nil || !strings.Contains(err.Error(), "decode state") {
		t.Fatalf("corrupt state err = %v", err)
	}
}

func TestOpenRejectsEmptyRootAndInvalidSeries(t *testing.T) {
	if _, err := Open("", "series"); err == nil || !strings.Contains(err.Error(), "output root is empty") {
		t.Fatalf("empty root err = %v", err)
	}
	for _, name := range []string{"", ".", "..", "a/b", `a\b`, "a\x00b"} {
		if _, err := Open(t.TempDir(), name); err == nil {
			t.Fatalf("series %q accepted", name)
		}
	}
}

func TestChapterDirRejectsUnsafeRawDisambiguator(t *testing.T) {
	series, err := Open(t.TempDir(), "series")
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"1/2", `1\2`, "1\x002"} {
		if _, err := series.ChapterDir("1", raw, 0, true); err == nil || !strings.Contains(err.Error(), "invalid raw chapter identity") {
			t.Fatalf("raw identity %q err = %v", raw, err)
		}
	}
}

func TestMaterializeDoneRequiresVolumeWhenNotFlat(t *testing.T) {
	series, err := Open(t.TempDir(), "series")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := series.MaterializeDone("1", "", 0, false); err == nil || !strings.Contains(err.Error(), "volume is required") {
		t.Fatalf("err = %v", err)
	}
}

func TestMaterializeDoneShortCircuitsOnExistingTarget(t *testing.T) {
	series, err := Open(t.TempDir(), "series")
	if err != nil {
		t.Fatal(err)
	}
	target, err := series.ChapterDir("2", "", 3, false)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeImage(t, filepath.Join(target, "001.jpg"), 1800)
	done, err := series.MaterializeDone("2", "", 3, false)
	if err != nil || !done {
		t.Fatalf("done = %v, err = %v", done, err)
	}
}

func TestMaterializeDoneLinksFlatModeFromVolumeSource(t *testing.T) {
	series, err := Open(t.TempDir(), "series")
	if err != nil {
		t.Fatal(err)
	}
	source, err := series.ChapterDir("9", "", 4, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"001.jpg", "002.jpg"} {
		writeResumeImage(t, filepath.Join(source, name), 1800)
	}
	if err := os.Mkdir(filepath.Join(source, "extras"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "notes.txt"), []byte("skip me"), 0o644); err != nil {
		t.Fatal(err)
	}
	done, err := series.MaterializeDone("9", "", 0, true)
	if err != nil || !done {
		t.Fatalf("done = %v, err = %v", done, err)
	}
	target := filepath.Join(series.Dir(), "chapter-009")
	for _, name := range []string{"001.jpg", "002.jpg"} {
		sourceInfo, err := os.Stat(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		targetInfo, err := os.Stat(filepath.Join(target, name))
		if err != nil {
			t.Fatalf("page %s not materialized: %v", name, err)
		}
		if !os.SameFile(sourceInfo, targetInfo) {
			t.Fatalf("page %s was copied instead of hard-linked", name)
		}
	}
	for _, name := range []string{"notes.txt", "extras"} {
		if _, err := os.Stat(filepath.Join(target, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("non-image entry %q materialized: %v", name, err)
		}
	}
}
