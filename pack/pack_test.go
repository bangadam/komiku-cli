package pack

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestConcurrentPackUsesIndependentPartFiles(t *testing.T) {
	root := t.TempDir()
	chapterDir := filepath.Join(root, "chapter-001")
	mustMkdir(t, chapterDir)
	writeImage(t, chapterDir, "001.jpg", append([]byte{0xff, 0xd8}, bytes.Repeat([]byte{1}, 64)...))
	volume := Volume{
		SeriesDir: root,
		Series:    "Series",
		Number:    1,
		Chapters:  []Chapter{{Number: 1, Display: "1", Dir: chapterDir, ExpectedPages: 1}},
	}

	originalRename := atomicRootRename
	defer func() { atomicRootRename = originalRename }()
	arrived := make(chan string, 2)
	release := make(chan struct{})
	atomicRootRename = func(root *os.Root, oldName, newName string) error {
		arrived <- oldName
		<-release
		return root.Rename(oldName, newName)
	}
	errorsByRun := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := RawVolume(volume)
			errorsByRun <- err
		}()
	}
	firstPart, secondPart := <-arrived, <-arrived
	if firstPart == secondPart {
		t.Fatalf("concurrent packs shared part file %q", firstPart)
	}
	close(release)
	group.Wait()
	close(errorsByRun)
	for err := range errorsByRun {
		if err != nil {
			t.Fatalf("concurrent pack failed: %v", err)
		}
	}
	archive := filepath.Join(root, "Series Volume 01.cbz")
	reader, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatalf("published archive is invalid: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	parts, err := filepath.Glob(filepath.Join(root, "*.part"))
	if err != nil || len(parts) != 0 {
		t.Fatalf("part files remain: %v err=%v", parts, err)
	}
}

func TestRawVolumeStoredBytesNamesAndNumericOrder(t *testing.T) {
	root := t.TempDir()
	chapter10 := filepath.Join(root, "chapter-010")
	chapter2 := filepath.Join(root, "chapter-002")
	mustMkdir(t, chapter10)
	mustMkdir(t, chapter2)
	jpeg := append([]byte{0xff, 0xd8}, []byte("jpeg-original")...)
	png := append([]byte("\x89PNG\r\n\x1a\n"), []byte("png-original")...)
	webp := append([]byte("RIFFxxxxWEBP"), []byte("webp-original")...)
	writeImage(t, chapter2, "001.jpeg", jpeg)
	writeImage(t, chapter2, "002.PNG", png)
	writeImage(t, chapter2, "003.webp", webp)
	for page := 4; page <= 10; page++ {
		writeImage(t, chapter2, pageName(page)+".jpg", append([]byte{0xff, 0xd8}, byte(page)))
	}
	writeImage(t, chapter10, "001.png", png)

	filename, err := RawVolume(Volume{
		SeriesDir: root,
		Series:    "Example Series",
		Number:    3,
		Chapters: []Chapter{
			{Number: 10, Display: "10", Dir: chapter10, ExpectedPages: 1},
			{Number: 2, Display: "2", Dir: chapter2, ExpectedPages: 10},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(filename) != "Example Series Volume 03.cbz" {
		t.Fatalf("archive name = %s", filename)
	}
	archive, err := zip.OpenReader(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	wantNames := []string{
		"Chapter 002/001.jpg", "Chapter 002/002.png", "Chapter 002/003.webp",
		"Chapter 002/004.jpg", "Chapter 002/005.jpg", "Chapter 002/006.jpg", "Chapter 002/007.jpg",
		"Chapter 002/008.jpg", "Chapter 002/009.jpg", "Chapter 002/010.jpg", "Chapter 010/001.png",
	}
	gotNames := make([]string, 0, len(archive.File))
	for _, entry := range archive.File {
		gotNames = append(gotNames, entry.Name)
		if entry.Method != zip.Store {
			t.Fatalf("%s method = %d, want ZIP_STORED", entry.Name, entry.Method)
		}
	}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("entry order = %#v, want %#v", gotNames, wantNames)
	}
	for name, want := range map[string][]byte{
		"Chapter 002/001.jpg":  jpeg,
		"Chapter 002/002.png":  png,
		"Chapter 002/003.webp": webp,
	} {
		if got := readEntry(t, archive.File, name); !bytes.Equal(got, want) {
			t.Fatalf("%s bytes changed: got %x want %x", name, got, want)
		}
	}
}

func TestRawVolumeRejectsIncompleteAndInvalidWithoutPublishing(t *testing.T) {
	root := t.TempDir()
	chapter := filepath.Join(root, "chapter-001")
	mustMkdir(t, chapter)
	writeImage(t, chapter, "001.jpg", append([]byte{0xff, 0xd8}, []byte("valid")...))
	final := filepath.Join(root, "Series Volume 01.cbz")
	old := []byte("existing-valid-final")
	if err := os.WriteFile(final, old, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := RawVolume(Volume{SeriesDir: root, Series: "Series", Number: 1, Chapters: []Chapter{{Number: 1, Display: "1", Dir: chapter, ExpectedPages: 2}}})
	if err == nil || !strings.Contains(err.Error(), "missing page 002") {
		t.Fatalf("incomplete volume error = %v", err)
	}
	assertFileBytes(t, final, old)
	assertNoPart(t, final)

	writeImage(t, chapter, "002.png", []byte("<html>not an image</html>"))
	_, err = RawVolume(Volume{SeriesDir: root, Series: "Series", Number: 1, Chapters: []Chapter{{Number: 1, Display: "1", Dir: chapter, ExpectedPages: 2}}})
	if err == nil || !strings.Contains(err.Error(), "invalid image") {
		t.Fatalf("invalid image error = %v", err)
	}
	assertFileBytes(t, final, old)
	assertNoPart(t, final)

	_, err = RawVolume(Volume{SeriesDir: root, Series: "Series", Number: 2})
	if err == nil || !strings.Contains(err.Error(), "no discovered chapters mapped") {
		t.Fatalf("missing chapter error = %v", err)
	}
	assertNoPart(t, filepath.Join(root, "Series Volume 02.cbz"))
}

func TestRawVolumeAtomicReplacementCleanupAndIdempotentRerun(t *testing.T) {
	root := t.TempDir()
	chapter := filepath.Join(root, "chapter-001")
	mustMkdir(t, chapter)
	image := append([]byte{0xff, 0xd8}, []byte("source")...)
	writeImage(t, chapter, "001.jpg", image)
	final := filepath.Join(root, "Series Volume 01.cbz")
	if err := os.WriteFile(final, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	volume := Volume{SeriesDir: root, Series: "Series", Number: 1, Chapters: []Chapter{{Number: 1, Display: "1", Dir: chapter, ExpectedPages: 1}}}

	originalRename := atomicRootRename
	atomicRootRename = func(*os.Root, string, string) error { return errors.New("injected rename failure") }
	_, err := RawVolume(volume)
	atomicRootRename = originalRename
	if err == nil || !strings.Contains(err.Error(), "injected rename failure") {
		t.Fatalf("rename error = %v", err)
	}
	assertFileBytes(t, final, []byte("old"))
	assertNoPart(t, final)

	if _, err := RawVolume(volume); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, []byte("old")) {
		t.Fatal("successful pack did not atomically replace old archive")
	}
	if _, err := RawVolume(volume); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("idempotent rerun changed deterministic archive bytes")
	}
	assertNoPart(t, final)
}

func TestPresetSettings(t *testing.T) {
	want := map[Preset]presetSettings{
		Medium: {longestSide: 1600, quality: 72},
		Small:  {longestSide: 1400, quality: 65},
		Tiny:   {longestSide: 1200, quality: 60},
		Raw:    {},
	}
	for preset, expected := range want {
		actual, err := preset.settings()
		if err != nil || actual != expected {
			t.Fatalf("%s settings=%+v err=%v, want %+v", preset, actual, err, expected)
		}
	}
	if _, err := Preset("large").settings(); err == nil {
		t.Fatal("unknown preset accepted")
	}
}

func TestPackVolumeConvertsFormatsBoundsAspectNoEnlargementAndBaseline(t *testing.T) {
	root := t.TempDir()
	chapter := filepath.Join(root, "chapter-001")
	mustMkdir(t, chapter)
	writeEncodedImage(t, filepath.Join(chapter, "001.jpg"), image.Rect(0, 0, 2000, 1000), "jpeg")
	writeEncodedImage(t, filepath.Join(chapter, "002.png"), image.Rect(0, 0, 1000, 2000), "png")
	webp, err := base64.StdEncoding.DecodeString("UklGRrIBAABXRUJQVlA4TKUBAAAvSsAYAA8w//M///MfeJAkbXvaSG7m8Q3GfYSBJekwQztm/IcZlgwnmWImn2BK7aFmBtnVir6q//8VOkFE/xm4baTIu8c48ArEo6+B3zFKYln3pqClSCKX0begFTAXFOLXHSyF8cCNcZEG4OywuA4KVVfJCiArU7GAgJI8+lJP/OKMT/fBAjevg1cYB7YVkFuWga2lyPi5I0HFy5YTpWIHg0RZpkniRVW9odHAKOwosWuOGdxIyn2OvaCDvhg/we6TwadPBPbqBV58MsLmMJ8yZnOWk8SRz4N+QoyPL+MnamzMvcE1rHNEr91F9GKZPVUcS9w7PhhH36suB9qPeYb/oLk6cuTiJ0wOK3m5h1cKjW6EVZCYMK7dxcKCBdgP9HkKr9gkAO2P8GKZGWVdIAatQa+1IDpt6qyorVwdy01xdW8Jkfk6xjEXmVQQ+HQdFr6OKhIN34dXWq0+0qr6EJSCeeVLH9+gvGTLyqM65PQ44ihzlTXxQKjKbAvshXgir7Lil9w4L2bvMycmjQcqXaMCO6BlY28i+FOLzbfI1vEqxAhotocAAA==")
	if err != nil {
		t.Fatal(err)
	}
	writeImage(t, chapter, "003.webp", webp)
	if _, _, err := image.Decode(bytes.NewReader(webp)); err != nil {
		t.Fatalf("WEBP fixture invalid: %v", err)
	}

	result, err := PackVolume(context.Background(), Volume{SeriesDir: root, Series: "Series", Number: 1, Chapters: []Chapter{{Number: 1, Display: "1", Dir: chapter, ExpectedPages: 3}}}, Medium)
	if err != nil {
		t.Fatal(err)
	}
	if result.Preset != Medium || len(result.Warnings) != 0 {
		t.Fatalf("result=%+v", result)
	}
	archive, err := zip.OpenReader(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	wantBounds := []image.Rectangle{image.Rect(0, 0, 1600, 800), image.Rect(0, 0, 800, 1600), image.Rect(0, 0, 75, 100)}
	for i, entry := range archive.File {
		wantName := "Chapter 001/00" + string(rune('1'+i)) + ".jpg"
		if entry.Name != wantName || entry.Method != zip.Store {
			t.Fatalf("entry %d name=%q method=%d", i, entry.Name, entry.Method)
		}
		body := readEntry(t, archive.File, entry.Name)
		decoded, format, err := image.Decode(bytes.NewReader(body))
		if err != nil || format != "jpeg" || decoded.Bounds() != wantBounds[i] {
			t.Fatalf("%s format=%s bounds=%v err=%v", entry.Name, format, decoded.Bounds(), err)
		}
		if !hasSOF0WithoutSOF2(body) {
			t.Fatalf("%s is not baseline SOF0 JPEG", entry.Name)
		}
	}
}

func TestPresetJPEGQualityPathAndDeterministicOutput(t *testing.T) {
	for _, preset := range []Preset{Medium, Small, Tiny} {
		t.Run(string(preset), func(t *testing.T) {
			root := t.TempDir()
			chapter := filepath.Join(root, "chapter-001")
			mustMkdir(t, chapter)
			filename := filepath.Join(chapter, "001.png")
			writeEncodedImage(t, filename, image.Rect(0, 0, 40, 30), "png")
			sourceFile, err := os.Open(filename)
			if err != nil {
				t.Fatal(err)
			}
			source, _, err := image.Decode(sourceFile)
			_ = sourceFile.Close()
			if err != nil {
				t.Fatal(err)
			}
			settings, _ := preset.settings()
			var expected bytes.Buffer
			if err := jpeg.Encode(&expected, source, &jpeg.Options{Quality: settings.quality}); err != nil {
				t.Fatal(err)
			}
			volume := Volume{SeriesDir: root, Series: "Series", Number: 1, Chapters: []Chapter{{Number: 1, Display: "1", Dir: chapter, ExpectedPages: 1}}}
			first, err := PackVolume(context.Background(), volume, preset)
			if err != nil {
				t.Fatal(err)
			}
			archive, err := zip.OpenReader(first.Path)
			if err != nil {
				t.Fatal(err)
			}
			actual := readEntry(t, archive.File, "Chapter 001/001.jpg")
			_ = archive.Close()
			if !bytes.Equal(actual, expected.Bytes()) {
				t.Fatalf("%s did not use JPEG quality %d", preset, settings.quality)
			}
			firstBytes, _ := os.ReadFile(first.Path)
			if _, err := PackVolume(context.Background(), volume, preset); err != nil {
				t.Fatal(err)
			}
			secondBytes, _ := os.ReadFile(first.Path)
			if !bytes.Equal(firstBytes, secondBytes) {
				t.Fatalf("%s archive output is not deterministic", preset)
			}
		})
	}
}

func TestPackVolumeRootConfinesAndRejectsSymlinkedSources(t *testing.T) {
	t.Run("source root symlink", func(t *testing.T) {
		output := t.TempDir()
		realRoot := t.TempDir()
		chapter := filepath.Join(realRoot, "chapter-001")
		mustMkdir(t, chapter)
		writeImage(t, chapter, "001.jpg", append([]byte{0xff, 0xd8}, make([]byte, 32)...))
		symlink := filepath.Join(t.TempDir(), "source")
		if err := os.Symlink(realRoot, symlink); err != nil {
			t.Fatal(err)
		}
		volume := Volume{SeriesDir: output, SourceRoot: symlink, Series: "Series", Number: 1, Chapters: []Chapter{{Number: 1, Display: "1", Dir: "chapter-001", ExpectedPages: 1}}}
		if _, err := RawVolume(volume); err == nil || !strings.Contains(err.Error(), "source root") {
			t.Fatalf("source root symlink err=%v", err)
		}
	})
	t.Run("chapter traversal", func(t *testing.T) {
		root := t.TempDir()
		outside := filepath.Join(t.TempDir(), "chapter-001")
		mustMkdir(t, outside)
		writeImage(t, outside, "001.jpg", append([]byte{0xff, 0xd8}, make([]byte, 32)...))
		volume := Volume{SeriesDir: root, SourceRoot: root, Series: "Series", Number: 1, Chapters: []Chapter{{Number: 1, Display: "1", Dir: filepath.Join("..", filepath.Base(filepath.Dir(outside)), "chapter-001"), ExpectedPages: 1}}}
		if _, err := RawVolume(volume); err == nil {
			t.Fatal("root traversal unexpectedly packed")
		}
	})
	t.Run("intermediate symlink", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		chapter := filepath.Join(outside, "chapter-001")
		mustMkdir(t, chapter)
		writeImage(t, chapter, "001.jpg", append([]byte{0xff, 0xd8}, make([]byte, 32)...))
		if err := os.Symlink(outside, filepath.Join(root, "alias")); err != nil {
			t.Fatal(err)
		}
		volume := Volume{SeriesDir: root, SourceRoot: root, Series: "Series", Number: 1, Chapters: []Chapter{{Number: 1, Display: "1", Dir: filepath.Join("alias", "chapter-001"), ExpectedPages: 1}}}
		if _, err := RawVolume(volume); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("intermediate symlink err=%v", err)
		}
	})
	t.Run("chapter symlink", func(t *testing.T) {
		root := t.TempDir()
		outside := filepath.Join(t.TempDir(), "chapter-001")
		mustMkdir(t, outside)
		writeImage(t, outside, "001.jpg", append([]byte{0xff, 0xd8}, make([]byte, 32)...))
		if err := os.Symlink(outside, filepath.Join(root, "chapter-001")); err != nil {
			t.Fatal(err)
		}
		volume := Volume{SeriesDir: root, SourceRoot: root, Series: "Series", Number: 1, Chapters: []Chapter{{Number: 1, Display: "1", Dir: "chapter-001", ExpectedPages: 1}}}
		if _, err := RawVolume(volume); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("chapter symlink err=%v", err)
		}
	})
	t.Run("page symlink", func(t *testing.T) {
		root := t.TempDir()
		chapter := filepath.Join(root, "chapter-001")
		mustMkdir(t, chapter)
		target := filepath.Join(t.TempDir(), "outside.jpg")
		if err := os.WriteFile(target, append([]byte{0xff, 0xd8}, make([]byte, 32)...), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(chapter, "001.jpg")); err != nil {
			t.Fatal(err)
		}
		volume := Volume{SeriesDir: root, SourceRoot: root, Series: "Series", Number: 1, Chapters: []Chapter{{Number: 1, Display: "1", Dir: "chapter-001", ExpectedPages: 1}}}
		if _, err := RawVolume(volume); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("page symlink err=%v", err)
		}
	})
}

func TestPackVolumePreflightRejectsUnsafeNamesAndOversizedPages(t *testing.T) {
	root := t.TempDir()
	chapter := filepath.Join(root, "chapter-001")
	mustMkdir(t, chapter)
	oversized := filepath.Join(chapter, "001.jpg")
	file, err := os.Create(oversized)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte{0xff, 0xd8}); err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(MaxPageBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	volume := Volume{SeriesDir: root, Series: "Series", Number: 1, Chapters: []Chapter{{Number: 1, Display: "1", Dir: chapter, ExpectedPages: 1}}}
	if _, err := RawVolume(volume); err == nil || !strings.Contains(err.Error(), "image size") {
		t.Fatalf("oversized page err=%v", err)
	}
	assertNoPart(t, filepath.Join(root, "Series Volume 01.cbz"))

	volume.Series = " bad\nname "
	if _, err := RawVolume(volume); err == nil || !strings.Contains(err.Error(), "invalid series name") {
		t.Fatalf("unsafe name err=%v", err)
	}
}

func TestPackVolumePreflightRejectsImageDimensionBomb(t *testing.T) {
	root := t.TempDir()
	chapter := filepath.Join(root, "chapter-001")
	mustMkdir(t, chapter)
	if err := os.WriteFile(filepath.Join(chapter, "001.png"), oversizedPNGHeader(MaxImageDimension+1, 1), 0o644); err != nil {
		t.Fatal(err)
	}
	volume := Volume{SeriesDir: root, Series: "Series", Number: 1, Chapters: []Chapter{{Number: 1, Display: "1", Dir: chapter, ExpectedPages: 1}}}
	for _, preset := range []Preset{Tiny, Raw} {
		if _, err := PackVolume(context.Background(), volume, preset); err == nil || !strings.Contains(err.Error(), "dimensions") {
			t.Fatalf("%s dimension bomb err=%v", preset, err)
		}
		assertNoPart(t, filepath.Join(root, "Series Volume 01.cbz"))
	}
}

func TestChapterPagesRejectsDirectorySwapWhileOpening(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "chapter-001")
	replacement := filepath.Join(root, "replacement")
	mustMkdir(t, original)
	mustMkdir(t, replacement)
	writeImage(t, original, "001.jpg", append([]byte{0xff, 0xd8}, make([]byte, 32)...))
	writeImage(t, replacement, "001.jpg", append([]byte{0xff, 0xd8}, make([]byte, 64)...))

	previous := afterChapterSourceInspect
	defer func() { afterChapterSourceInspect = previous }()
	afterChapterSourceInspect = func(string) {
		if err := os.Rename(original, filepath.Join(root, "old")); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, original); err != nil {
			t.Fatal(err)
		}
	}
	volume := Volume{SeriesDir: root, SourceRoot: root, Series: "Series", Number: 1, Chapters: []Chapter{{Number: 1, Display: "1", Dir: "chapter-001", ExpectedPages: 1}}}
	if _, err := RawVolume(volume); err == nil || !strings.Contains(err.Error(), "changed while opening") {
		t.Fatalf("directory swap err=%v", err)
	}
}

func TestOpenRegularSourceRejectsIdentitySwap(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "001.jpg")
	writeImage(t, root, "001.jpg", append([]byte{0xff, 0xd8}, make([]byte, 32)...))
	expected, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(root, "replacement.jpg")
	writeImage(t, root, "replacement.jpg", append([]byte{0xff, 0xd8}, make([]byte, 64)...))
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if file, _, err := openRegularSource(nil, path, expected); err == nil {
		file.Close()
		t.Fatal("identity swap unexpectedly opened")
	}
}

func TestPackVolumeCorruptMagicValidFallsBackWithWarning(t *testing.T) {
	root := t.TempDir()
	chapter := filepath.Join(root, "chapter-001")
	mustMkdir(t, chapter)
	corrupt := append([]byte{0xff, 0xd8}, []byte("truncated source")...)
	writeImage(t, chapter, "001.jpeg", corrupt)
	result, err := PackVolume(context.Background(), Volume{SeriesDir: root, Series: "Series", Number: 1, Chapters: []Chapter{{Number: 1, Display: "1", Dir: chapter, ExpectedPages: 1}}}, Tiny)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Entry != "Chapter 001/001.jpg" || !strings.Contains(result.Warnings[0].Error(), "copied original") {
		t.Fatalf("warnings=%+v", result.Warnings)
	}
	archive, err := zip.OpenReader(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if len(archive.File) != 1 || archive.File[0].Name != "Chapter 001/001.jpg" || archive.File[0].Method != zip.Store || !bytes.Equal(readEntry(t, archive.File, archive.File[0].Name), corrupt) {
		t.Fatalf("fallback entry=%+v", archive.File)
	}
	assertNoPart(t, result.Path)
}
func TestPackVolumeCancelledLeavesFinalAndCleansPart(t *testing.T) {
	root := t.TempDir()
	chapter := filepath.Join(root, "chapter-001")
	mustMkdir(t, chapter)
	writeEncodedImage(t, filepath.Join(chapter, "001.png"), image.Rect(0, 0, 20, 20), "png")
	final := filepath.Join(root, "Series Volume 01.cbz")
	old := []byte("old archive")
	if err := os.WriteFile(final, old, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := PackVolume(ctx, Volume{SeriesDir: root, Series: "Series", Number: 1, Chapters: []Chapter{{Number: 1, Display: "1", Dir: chapter, ExpectedPages: 1}}}, Medium)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
	assertFileBytes(t, final, old)
	assertNoPart(t, final)
}

func writeEncodedImage(t *testing.T, filename string, bounds image.Rectangle, format string) {
	t.Helper()
	img := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: uint8(x + y), A: 255})
		}
	}
	file, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	if format == "png" {
		err = png.Encode(file, img)
	} else {
		err = jpeg.Encode(file, img, &jpeg.Options{Quality: 90})
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
}

func hasSOF0WithoutSOF2(data []byte) bool {
	sof0 := false
	for i := 2; i+1 < len(data); i++ {
		if data[i] != 0xff {
			continue
		}
		switch data[i+1] {
		case 0xc0:
			sof0 = true
		case 0xc2:
			return false
		}
	}
	return sof0
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeImage(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func pageName(page int) string {
	return string([]byte{'0' + byte(page/100), '0' + byte(page/10%10), '0' + byte(page%10)})
}

func readEntry(t *testing.T, entries []*zip.File, name string) []byte {
	t.Helper()
	for _, entry := range entries {
		if entry.Name != name {
			continue
		}
		reader, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		data, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	t.Fatalf("entry %s not found", name)
	return nil
}

func assertFileBytes(t *testing.T, filename string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s bytes = %q, want %q", filename, got, want)
	}
}

func oversizedPNGHeader(width, height int) []byte {
	data := make([]byte, 13)
	binary.BigEndian.PutUint32(data[0:4], uint32(width))
	binary.BigEndian.PutUint32(data[4:8], uint32(height))
	data[8], data[9] = 8, 2
	chunk := append([]byte("IHDR"), data...)
	output := append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\r"), chunk...)
	var checksum [4]byte
	binary.BigEndian.PutUint32(checksum[:], crc32.ChecksumIEEE(chunk))
	return append(output, checksum[:]...)
}

func assertNoPart(t *testing.T, final string) {
	t.Helper()
	if _, err := os.Stat(final + ".part"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy part file remains: %v", err)
	}
	parts, err := filepath.Glob(final + ".*.part")
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 0 {
		t.Fatalf("unique part files remain: %v", parts)
	}
}
