package cli

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bangadam/komiku-cli/komiku"
	packer "github.com/bangadam/komiku-cli/pack"
	"github.com/bangadam/komiku-cli/store"
)

func TestPreparePackUsesLocalSmallPagesAndExplainsMissingPages(t *testing.T) {
	seriesStore, err := store.Open(t.TempDir(), "series")
	if err != nil {
		t.Fatal(err)
	}
	chapter := komiku.Chapter{RawID: "1", Display: "1", Number: 1, URL: "https://fixture.invalid/chapter/actual/"}
	job := Job{Chapter: chapter, Volume: 1}
	dir, err := seriesStore.ChapterDir(chapter.Display, "", 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for page := 1; page <= 2; page++ {
		data := make([]byte, 1800)
		copy(data, []byte{0xff, 0xd8})
		if err := os.WriteFile(filepath.Join(dir, string(rune('0'+page))+".jpg"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mapping := []komiku.Volume{{Volume: 1, Start: 1, End: 1}}
	result := []Result{{Chapter: chapter, Status: Done, Total: 2, Success: 2}}
	plan := PreparePack(seriesStore, "Series", packer.Raw, mapping, []Job{job}, result)
	if plan.DisabledReason != "" || len(plan.Skipped) != 0 || len(plan.Volumes) != 1 || plan.Volumes[0].Chapters[0].ExpectedPages != 2 {
		t.Fatalf("eligible plan=%+v", plan)
	}

	if err := os.Remove(filepath.Join(dir, "2.jpg")); err != nil {
		t.Fatal(err)
	}
	plan = PreparePack(seriesStore, "Series", packer.Raw, mapping, []Job{job}, result)
	if len(plan.Volumes) != 0 || len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0].Reason, "has 1 of 2 expected pages") {
		t.Fatalf("missing-page plan=%+v", plan)
	}
}

func TestPreparePackRequiresMappedDoneVolumes(t *testing.T) {
	seriesStore, _ := store.Open(t.TempDir(), "series")
	if plan := PreparePack(seriesStore, "Series", packer.Raw, nil, nil, nil); plan.DisabledReason != "Pack needs a volume mapping." {
		t.Fatalf("reason=%q", plan.DisabledReason)
	}
	mapping := []komiku.Volume{{Volume: 1, Start: 1, End: 1}}
	chapter := komiku.Chapter{RawID: "1", Display: "1", Number: 1, URL: "https://fixture.invalid/chapter/actual/"}
	plan := PreparePack(seriesStore, "Series", packer.Raw, mapping, []Job{{Chapter: chapter, Volume: 1}}, []Result{{Chapter: chapter, Status: Part}})
	if len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0].Reason, "not completely downloaded") {
		t.Fatalf("reason=%+v", plan)
	}
}

func TestPreparePackReportsIntentionallyIncompleteMappedSelection(t *testing.T) {
	seriesStore, _ := store.Open(t.TempDir(), "series")
	mapping := []komiku.Volume{{Volume: 1, Start: 1, End: 2}}
	chapter := komiku.Chapter{RawID: "1", Display: "1", Number: 1, URL: "one"}
	plan := PreparePack(seriesStore, "Series", packer.Raw, mapping, []Job{{Chapter: chapter, Volume: 1}}, []Result{{Chapter: chapter, Status: Done}})
	if len(plan.Volumes) != 0 || len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0].Reason, "chapter 2") {
		t.Fatalf("incomplete mapped selection plan=%+v", plan)
	}
}

func TestPackPreparedVolumesReturnsEveryIndependentOutcome(t *testing.T) {
	root := t.TempDir()
	validDir := filepath.Join(root, "valid")
	missingDir := filepath.Join(root, "missing")
	warningDir := filepath.Join(root, "warning")
	for _, dir := range []string{validDir, missingDir, warningDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	valid, err := os.Create(filepath.Join(validDir, "001.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	imageData := image.NewRGBA(image.Rect(0, 0, 2, 2))
	imageData.Set(0, 0, color.White)
	if err := jpeg.Encode(valid, imageData, nil); err != nil {
		valid.Close()
		t.Fatal(err)
	}
	if err := valid.Close(); err != nil {
		t.Fatal(err)
	}
	corrupt := make([]byte, 1800)
	copy(corrupt, []byte{0xff, 0xd8})
	if err := os.WriteFile(filepath.Join(warningDir, "001.jpg"), corrupt, 0o644); err != nil {
		t.Fatal(err)
	}
	plan := PackPlan{Preset: packer.Medium, Volumes: []packer.Volume{
		{SeriesDir: root, Series: "Series", Number: 1, Chapters: []packer.Chapter{{Number: 1, Display: "1", Dir: validDir, ExpectedPages: 1}}},
		{SeriesDir: root, Series: "Series", Number: 2, Chapters: []packer.Chapter{{Number: 2, Display: "2", Dir: missingDir, ExpectedPages: 1}}},
		{SeriesDir: root, Series: "Series", Number: 3, Chapters: []packer.Chapter{{Number: 3, Display: "3", Dir: warningDir, ExpectedPages: 1}}},
	}}
	outcomes, err := PackPreparedVolumes(context.Background(), plan)
	if err != nil || len(outcomes) != 3 {
		t.Fatalf("outcomes=%+v err=%v", outcomes, err)
	}
	if outcomes[0].Err != nil || len(outcomes[0].Result.Warnings) != 0 {
		t.Fatalf("success outcome=%+v", outcomes[0])
	}
	if outcomes[1].Err == nil {
		t.Fatalf("failure outcome=%+v", outcomes[1])
	}
	if outcomes[2].Err != nil || len(outcomes[2].Result.Warnings) != 1 {
		t.Fatalf("warning outcome=%+v", outcomes[2])
	}
	for _, volume := range []int{1, 3} {
		path := filepath.Join(root, "Series Volume 0"+string(rune('0'+volume))+".cbz")
		if info, statErr := os.Stat(path); statErr != nil || info.Size() == 0 {
			t.Fatalf("volume %d archive missing: %v", volume, statErr)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "Series Volume 02.cbz.part")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed volume left .part: %v", err)
	}
}

func TestPackPreparedVolumesCancellationHasNoFinalPart(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	plan := PackPlan{Preset: packer.Raw, Volumes: []packer.Volume{{SeriesDir: root, Series: "Series", Number: 1}}}
	outcomes, err := PackPreparedVolumes(ctx, plan)
	if len(outcomes) != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("outcomes=%+v err=%v", outcomes, err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "Series Volume 01.cbz.part")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("cancel left .part: %v", statErr)
	}
}
