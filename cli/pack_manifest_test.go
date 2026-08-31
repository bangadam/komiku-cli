package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bangadam/komiku-cli/komiku"
	packer "github.com/bangadam/komiku-cli/pack"
)

func TestResolveCompleteVolumeSelectionIsStrictAndAtomic(t *testing.T) {
	chapters := []komiku.Chapter{
		{RawID: "1", Display: "1", Number: 1, URL: "one"},
		{RawID: "2", Display: "2", Number: 2, URL: "two"},
		{RawID: "3", Display: "3", Number: 3, URL: "three"},
		{RawID: "4", Display: "4", Number: 4, URL: "four"},
		{RawID: "4.5", Display: "4.5", Number: 4.5, URL: "extra"},
	}
	rows := []komiku.Volume{{Volume: 1, Start: 1, End: 2}, {Volume: 2, Start: 3, End: 4}}

	resolved, err := ResolveCompleteVolumeSelection(chapters, map[string]bool{"one": true, "two": true, "three": true, "four": true}, rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Mappings) != 2 || len(resolved.Jobs) != 4 || resolved.Jobs[0].Volume != 1 || resolved.Jobs[2].Volume != 2 {
		t.Fatalf("resolved=%+v", resolved)
	}

	tests := []struct {
		name     string
		chapters []komiku.Chapter
		selected map[string]bool
		rows     []komiku.Volume
		want     string
	}{
		{"partial", chapters, map[string]bool{"one": true}, rows, "requires selected chapter 2"},
		{"extra", chapters, map[string]bool{"one": true, "two": true, "extra": true}, rows, "extra or non-integer"},
		{"missing discovered", chapters[:1], map[string]bool{"one": true}, rows[:1], "was not discovered"},
		{"ambiguous", append(append([]komiku.Chapter(nil), chapters[:2]...), komiku.Chapter{RawID: "02", Display: "2", Number: 2, URL: "two-alt"}), map[string]bool{"one": true, "two": true}, rows[:1], "ambiguous discovered identities"},
		{"overlap", chapters, map[string]bool{"one": true, "two": true}, []komiku.Volume{{Volume: 1, Start: 1, End: 2}, {Volume: 2, Start: 2, End: 3}}, "overlap"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveCompleteVolumeSelection(test.chapters, test.selected, test.rows)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("result=%+v err=%v, want %q", got, err, test.want)
			}
			if len(got.Jobs) != 0 || len(got.Mappings) != 0 {
				t.Fatalf("strict rejection leaked partial assignment: %+v", got)
			}
		})
	}
}

func TestResolveCompleteVolumeSelectionIgnoresAmbiguityOutsideChosenRows(t *testing.T) {
	chapters := []komiku.Chapter{
		{RawID: "1", Display: "1", Number: 1, URL: "one"},
		{RawID: "2", Display: "2", Number: 2, URL: "two"},
		{RawID: "20-a", Display: "20", Number: 20, URL: "twenty-a"},
		{RawID: "20-b", Display: "20", Number: 20, URL: "twenty-b"},
	}
	rows := []komiku.Volume{{Volume: 1, Start: 1, End: 2}, {Volume: 20, Start: 20, End: 20}}
	resolved, err := ResolveCompleteVolumeSelection(chapters, map[string]bool{"one": true, "two": true}, rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Mappings) != 1 || resolved.Mappings[0].Volume != 1 || len(resolved.Jobs) != 2 {
		t.Fatalf("resolved=%+v", resolved)
	}
}

func TestRecordPackManifestIgnoresDoneFractionalExtras(t *testing.T) {
	seriesDir := t.TempDir()
	mapping := []komiku.Volume{{Volume: 1, Start: 1, End: 2}}
	chapters := []komiku.Chapter{
		{RawID: "1", Display: "1", Number: 1, URL: "one"},
		{RawID: "2", Display: "2", Number: 2, URL: "two"},
		{RawID: "2.5", Display: "2.5", Number: 2.5, URL: "extra"},
	}
	jobs := make([]Job, 0, len(chapters))
	results := make([]Result, 0, len(chapters))
	for _, chapter := range chapters {
		dir := filepath.Join(seriesDir, "chapter-"+chapter.Display)
		writeManifestTestImage(t, dir, "001.jpg")
		jobs = append(jobs, Job{Chapter: chapter, Volume: 1})
		results = append(results, Result{Chapter: chapter, Status: Done, Success: 1, Total: 1, SourceDir: dir})
	}
	if err := RecordPackManifest(seriesDir, "series", "", "wikipedia-display", mapping, jobs, results); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadPackManifest(seriesDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Chapters) != 2 || manifest.Chapters[0].Number != 1 || manifest.Chapters[1].Number != 2 {
		t.Fatalf("fractional extra leaked into manifest: %+v", manifest.Chapters)
	}
	plan := PreparePackSources(seriesDir, "series", packer.Raw, mapping, []PackChapterSource{
		{Chapter: chapters[0], Volume: 1, Dir: results[0].SourceDir, ExpectedPages: 1, Complete: true},
		{Chapter: chapters[1], Volume: 1, Dir: results[1].SourceDir, ExpectedPages: 1, Complete: true},
		{Chapter: chapters[2], Volume: 1, Dir: results[2].SourceDir, ExpectedPages: 1, Complete: true},
	})
	if plan.DisabledReason != "" || len(plan.Volumes) != 1 || len(plan.Volumes[0].Chapters) != 2 {
		t.Fatalf("fractional extra disabled packing: %+v", plan)
	}
}

func TestPackManifestIsAtomicDeterministicRelocatableAndOffline(t *testing.T) {
	parent := t.TempDir()
	seriesDir := filepath.Join(parent, "series")
	if err := os.MkdirAll(seriesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	chapters := []komiku.Chapter{
		{RawID: "1", Display: "1", Number: 1, URL: "one"},
		{RawID: "2", Display: "2", Number: 2, URL: "two"},
	}
	jobs := make([]Job, 0, len(chapters))
	results := make([]Result, 0, len(chapters))
	for _, chapter := range chapters {
		dir := filepath.Join(seriesDir, "vol-01", "chapter-00"+chapter.Display)
		writeManifestTestImage(t, dir, "001.jpg")
		jobs = append(jobs, Job{Chapter: chapter, Volume: 1})
		results = append(results, Result{Chapter: chapter, Status: Done, Success: 1, Total: 1, SourceDir: dir})
	}
	mappings := []komiku.Volume{{Volume: 1, Start: 1, End: 2}}
	if err := RecordPackManifest(seriesDir, "series", "https://komiku.org/manga/series/", "wikipedia-display", mappings, jobs, results); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(PackManifestPath(seriesDir))
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordPackManifest(seriesDir, "series", "https://komiku.org/manga/series/", "wikipedia-display", mappings, jobs, results); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(PackManifestPath(seriesDir))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("identical manifest merge was nondeterministic:\n%s\n%s", first, second)
	}
	manifest, err := LoadPackManifest(seriesDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Chapters) != 2 || filepath.IsAbs(manifest.Chapters[0].SourceDir) {
		t.Fatalf("manifest is not complete and relocatable: %+v", manifest)
	}

	movedParent := t.TempDir()
	movedSeries := filepath.Join(movedParent, "series")
	if err := os.Rename(seriesDir, movedSeries); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(movedSeries, "vol-01", "chapter-001", "001.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PrepareManifestPack(movedSeries, packer.Raw, "")
	if err != nil {
		t.Fatal(err)
	}
	outcomes, err := PackPreparedVolumes(context.Background(), plan)
	if err != nil || len(outcomes) != 1 || outcomes[0].Err != nil {
		t.Fatalf("offline relocated pack outcomes=%+v err=%v", outcomes, err)
	}
	after, err := os.ReadFile(filepath.Join(movedSeries, "vol-01", "chapter-001", "001.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("offline pack rewrote source image")
	}
}

func TestPackManifestRejectsConflictTraversalSymlinksAndStalePages(t *testing.T) {
	t.Run("conflict", func(t *testing.T) {
		seriesDir := t.TempDir()
		chapter := komiku.Chapter{RawID: "1", Display: "1", Number: 1, URL: "one"}
		dir := filepath.Join(seriesDir, "vol-01", "chapter-001")
		writeManifestTestImage(t, dir, "001.jpg")
		mapping := []komiku.Volume{{Volume: 1, Start: 1, End: 1}}
		job := []Job{{Chapter: chapter, Volume: 1}}
		result := []Result{{Chapter: chapter, Status: Done, Success: 1, Total: 1, SourceDir: dir}}
		if err := RecordPackManifest(seriesDir, "series", "", "wikipedia-display", mapping, job, result); err != nil {
			t.Fatal(err)
		}
		writeManifestTestImage(t, dir, "002.jpg")
		result[0].Total, result[0].Success = 2, 2
		if err := RecordPackManifest(seriesDir, "series", "", "wikipedia-display", mapping, job, result); err == nil || !strings.Contains(err.Error(), "conflicts") {
			t.Fatalf("manifest conflict err=%v", err)
		}
	})
	t.Run("traversal", func(t *testing.T) {
		seriesDir := t.TempDir()
		manifest := `{"version":1,"series":"series","mappings":[{"volume":1,"start":1,"end":1,"provenance":"wikipedia-display"}],"chapters":[{"raw_id":"1","display":"1","number":1,"volume":1,"source_dir":"../outside","expected_pages":1,"completed":true}]}`
		if err := os.WriteFile(PackManifestPath(seriesDir), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareManifestPack(seriesDir, packer.Raw, "1"); err == nil || !strings.Contains(err.Error(), "escapes series root") {
			t.Fatalf("traversal err=%v", err)
		}
	})
	t.Run("intermediate source symlink", func(t *testing.T) {
		seriesDir := t.TempDir()
		outside := t.TempDir()
		writeManifestTestImage(t, filepath.Join(outside, "chapter-001"), "001.jpg")
		if err := os.Symlink(outside, filepath.Join(seriesDir, "alias")); err != nil {
			t.Fatal(err)
		}
		if _, _, err := validatePackSource(seriesDir, filepath.Join("alias", "chapter-001")); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("intermediate source symlink err=%v", err)
		}
	})
	t.Run("symlink and stale pages", func(t *testing.T) {
		seriesDir := t.TempDir()
		chapter := komiku.Chapter{RawID: "1", Display: "1", Number: 1, URL: "one"}
		dir := filepath.Join(seriesDir, "vol-01", "chapter-001")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside.jpg")
		if err := os.WriteFile(outside, append([]byte{0xff, 0xd8}, make([]byte, 32)...), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(dir, "001.jpg")); err != nil {
			t.Fatal(err)
		}
		mapping := []komiku.Volume{{Volume: 1, Start: 1, End: 1}}
		job := []Job{{Chapter: chapter, Volume: 1}}
		result := []Result{{Chapter: chapter, Status: Done, Success: 1, Total: 1, SourceDir: dir}}
		if err := RecordPackManifest(seriesDir, "series", "", "wikipedia-display", mapping, job, result); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("symlink err=%v", err)
		}
		if err := os.Remove(filepath.Join(dir, "001.jpg")); err != nil {
			t.Fatal(err)
		}
		writeManifestTestImage(t, dir, "001.jpg")
		if err := RecordPackManifest(seriesDir, "series", "", "wikipedia-display", mapping, job, result); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(dir, "001.jpg")); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareManifestPack(seriesDir, packer.Raw, "1"); err == nil {
			t.Fatal("stale missing page was accepted")
		}
	})
}

func TestPackManifestConcurrentMergeDoesNotLoseVolumes(t *testing.T) {
	seriesDir := t.TempDir()
	var group sync.WaitGroup
	errs := make(chan error, 2)
	for volume := 1; volume <= 2; volume++ {
		volume := volume
		chapter := komiku.Chapter{RawID: string(rune('0' + volume)), Display: string(rune('0' + volume)), Number: float64(volume), URL: string(rune('a' + volume))}
		dir := filepath.Join(seriesDir, "vol-0"+chapter.Display, "chapter-00"+chapter.Display)
		writeManifestTestImage(t, dir, "001.jpg")
		group.Add(1)
		go func() {
			defer group.Done()
			errs <- RecordPackManifest(seriesDir, "series", "", "wikipedia-display", []komiku.Volume{{Volume: volume, Start: volume, End: volume}}, []Job{{Chapter: chapter, Volume: volume}}, []Result{{Chapter: chapter, Status: Done, Success: 1, Total: 1, SourceDir: dir}})
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := LoadPackManifest(seriesDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Mappings) != 2 || len(manifest.Chapters) != 2 {
		t.Fatalf("concurrent merge lost data: %+v", manifest)
	}
}

func TestPackManifestRejectsSymlinkLockFile(t *testing.T) {
	seriesDir := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside-lock")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, PackManifestPath(seriesDir)+".lock"); err != nil {
		t.Fatal(err)
	}
	chapter := komiku.Chapter{RawID: "1", Display: "1", Number: 1, URL: "one"}
	if err := RecordPackManifest(seriesDir, "series", "", "wikipedia-display", []komiku.Volume{{Volume: 1, Start: 1, End: 1}}, []Job{{Chapter: chapter, Volume: 1}}, nil); err == nil || !strings.Contains(err.Error(), "non-symlink") {
		t.Fatalf("lock symlink err=%v", err)
	}
}

func TestManifestLockBlocksAnotherProcessAndSurvivesCrashArtifact(t *testing.T) {
	if os.Getenv("KOMIKU_MANIFEST_LOCK_HELPER") != "" {
		release, err := acquireManifestLock(os.Getenv("KOMIKU_MANIFEST_LOCK_DIR"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stdout, "locked")
		if os.Getenv("KOMIKU_MANIFEST_LOCK_HOLD") != "" {
			time.Sleep(250 * time.Millisecond)
		}
		release()
		os.Exit(0)
	}

	seriesDir := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestManifestLockBlocksAnotherProcessAndSurvivesCrashArtifact$")
	command.Env = append(os.Environ(), "KOMIKU_MANIFEST_LOCK_HELPER=1", "KOMIKU_MANIFEST_LOCK_HOLD=1", "KOMIKU_MANIFEST_LOCK_DIR="+seriesDir)
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, len("locked\n"))
	if _, err := io.ReadFull(output, buffer); err != nil || string(buffer) != "locked\n" {
		t.Fatalf("helper lock signal=%q err=%v", buffer, err)
	}
	start := time.Now()
	release, err := acquireManifestLock(seriesDir)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Fatalf("cross-process lock was not exclusive: elapsed=%s", elapsed)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestPackManifestReclaimsUnlockedLockFile(t *testing.T) {
	seriesDir := t.TempDir()
	if err := os.WriteFile(PackManifestPath(seriesDir)+".lock", []byte("stale crash artifact\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	chapter := komiku.Chapter{RawID: "1", Display: "1", Number: 1, URL: "one"}
	dir := filepath.Join(seriesDir, "chapter-001")
	writeManifestTestImage(t, dir, "001.jpg")
	if err := RecordPackManifest(seriesDir, "series", "", "wikipedia-display", []komiku.Volume{{Volume: 1, Start: 1, End: 1}}, []Job{{Chapter: chapter, Volume: 1}}, []Result{{Chapter: chapter, Status: Done, Success: 1, Total: 1, SourceDir: dir}}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPackManifest(seriesDir); err != nil {
		t.Fatal(err)
	}
}

func TestPackManifestRejectsUntrustedJSONAndDuplicateSources(t *testing.T) {
	t.Run("duplicate keys", func(t *testing.T) {
		root := t.TempDir()
		data := `{"version":1,"version":1,"series":"series","mappings":[],"chapters":[]}`
		if err := os.WriteFile(PackManifestPath(root), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadPackManifest(root); err == nil || !strings.Contains(err.Error(), "duplicate object key") {
			t.Fatalf("duplicate-key err=%v", err)
		}
	})
	t.Run("oversized", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(PackManifestPath(root), bytes.Repeat([]byte{' '}, int(maxPackManifestBytes)+1), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadPackManifest(root); err == nil || !strings.Contains(err.Error(), "byte limit") {
			t.Fatalf("oversize err=%v", err)
		}
	})
	t.Run("manifest symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(t.TempDir(), "outside.json")
		if err := os.WriteFile(target, []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, PackManifestPath(root)); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadPackManifest(root); err == nil || !strings.Contains(err.Error(), "non-symlink") {
			t.Fatalf("manifest symlink err=%v", err)
		}
	})
	t.Run("same source claimed twice", func(t *testing.T) {
		root := t.TempDir()
		manifest := PackManifest{
			Version:  PackManifestVersion,
			Series:   "series",
			Mappings: []PackManifestMapping{{Volume: 1, Start: 1, End: 2, Provenance: "wikipedia-display"}},
			Chapters: []PackManifestChapter{
				{URL: "one", RawID: "1", IdentityProvenance: "komiku-download", Display: "1", Number: 1, Volume: 1, SourceDir: "same", ExpectedPages: 1, Completed: true},
				{URL: "two", RawID: "2", IdentityProvenance: "komiku-download", Display: "2", Number: 2, Volume: 1, SourceDir: "same", ExpectedPages: 1, Completed: true},
			},
		}
		data, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(PackManifestPath(root), data, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadPackManifest(root); err == nil || !strings.Contains(err.Error(), "claimed by multiple") {
			t.Fatalf("duplicate source err=%v", err)
		}
	})
}

func TestRecoveredManifestCommitRemovesPublishedManifestWhenDirectorySyncFails(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "chapter-001")
	writeManifestTestImage(t, source, "001.jpg")
	transaction, err := prepareRecoveredPackManifest(root, "series", []komiku.Volume{{Volume: 1, Start: 1, End: 1}}, []PackChapterSource{{
		Chapter: komiku.Chapter{Display: "1", Number: 1}, Volume: 1, Dir: source, ExpectedPages: 1, Complete: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Abort()
	calls := 0
	transaction.syncDirectory = func(string) error {
		calls++
		if calls == 1 {
			return errors.New("injected directory sync failure")
		}
		return nil
	}
	if err := transaction.Commit(); err == nil || !strings.Contains(err.Error(), "injected directory sync failure") {
		t.Fatalf("commit err=%v", err)
	}
	if calls != 2 {
		t.Fatalf("sync calls=%d, want publish and rollback sync", calls)
	}
	if transaction.HasPublishedManifest() {
		t.Fatal("rolled-back transaction still reports a published manifest")
	}
	if _, err := os.Lstat(PackManifestPath(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed commit left published manifest: %v", err)
	}
}

func TestRecoveredManifestCommitReportsPublishedManifestWhenRollbackRemovalFails(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "chapter-001")
	writeManifestTestImage(t, source, "001.jpg")
	transaction, err := prepareRecoveredPackManifest(root, "series", []komiku.Volume{{Volume: 1, Start: 1, End: 1}}, []PackChapterSource{{
		Chapter: komiku.Chapter{Display: "1", Number: 1}, Volume: 1, Dir: source, ExpectedPages: 1, Complete: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Abort()
	transaction.syncDirectory = func(string) error { return errors.New("injected directory sync failure") }
	transaction.removeManifest = func(string) error { return errors.New("injected remove failure") }
	if err := transaction.Commit(); err == nil || !strings.Contains(err.Error(), "injected remove failure") {
		t.Fatalf("commit err=%v", err)
	}
	if !transaction.HasPublishedManifest() {
		t.Fatal("transaction hid manifest after rollback removal failed")
	}
	if _, err := os.Stat(PackManifestPath(root)); err != nil {
		t.Fatalf("published manifest missing: %v", err)
	}
}

func TestRecoveredManifestUsesLocalIdentityAndRootConfinedSources(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "chapter-001")
	writeManifestTestImage(t, source, "001.jpg")
	mappings := []komiku.Volume{{Volume: 1, Start: 1, End: 1}}
	sources := []PackChapterSource{{Chapter: komiku.Chapter{Display: "1", Number: 1}, Volume: 1, Dir: source, ExpectedPages: 1, Complete: true}}
	if err := RecordRecoveredPackManifest(root, "series", mappings, sources); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadPackManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Chapters) != 1 || manifest.Chapters[0].IdentityProvenance != "recovered-local" || manifest.Chapters[0].URL != "" || manifest.Chapters[0].RawID != "" {
		t.Fatalf("recovered identity=%+v", manifest.Chapters)
	}
	plan, err := PrepareManifestPack(root, packer.Raw, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Volumes) != 1 || plan.Volumes[0].SourceRoot == "" || filepath.IsAbs(plan.Volumes[0].Chapters[0].Dir) {
		t.Fatalf("recovered plan is not root-confined: %+v", plan)
	}
}

func TestPackSourceSparsePageNumberIsBounded(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "chapter-001")
	writeManifestTestImage(t, source, "999999999.jpg")
	if _, _, err := validatePackSource(root, "chapter-001"); err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("sparse page err=%v", err)
	}
}

func TestManifestPackRequiresExplicitScopeWhenDeclaredVolumeIsIncomplete(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "chapter-001")
	writeManifestTestImage(t, source, "001.jpg")
	if err := RecordRecoveredPackManifest(root, "series", []komiku.Volume{{Volume: 1, Start: 1, End: 1}}, []PackChapterSource{{Chapter: komiku.Chapter{Display: "1", Number: 1}, Volume: 1, Dir: source, ExpectedPages: 1, Complete: true}}); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadPackManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Mappings = append(manifest.Mappings, PackManifestMapping{Volume: 2, Start: 2, End: 2, Provenance: "wikipedia-recovery"})
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(PackManifestPath(root), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareManifestPack(root, packer.Raw, ""); err == nil || !strings.Contains(err.Error(), "use --vol") {
		t.Fatalf("ambiguous default err=%v", err)
	}
	plan, err := PrepareManifestPack(root, packer.Raw, "1")
	if err != nil || len(plan.Volumes) != 1 || plan.Volumes[0].Number != 1 {
		t.Fatalf("explicit complete plan=%+v err=%v", plan, err)
	}
}

func TestStrictVolumeIndexRejectsHugeOrExcessCoverage(t *testing.T) {
	if _, _, err := strictVolumeChapterIndex([]komiku.Volume{{Volume: 1, Start: 1, End: int(^uint(0) >> 1)}}); err == nil {
		t.Fatal("max-int range unexpectedly accepted")
	}
	rows := make([]komiku.Volume, 0, 2)
	rows = append(rows,
		komiku.Volume{Volume: 1, Start: 1, End: 6000},
		komiku.Volume{Volume: 2, Start: 6001, End: 11000},
	)
	if _, _, err := strictVolumeChapterIndex(rows); err == nil || !strings.Contains(err.Error(), "covers more") {
		t.Fatalf("coverage limit err=%v", err)
	}
}

func writeManifestTestImage(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), append([]byte{0xff, 0xd8}, make([]byte, 32)...), 0o644); err != nil {
		t.Fatal(err)
	}
}
