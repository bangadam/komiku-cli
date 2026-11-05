// reader_chapters.go discovers local chapters and local page images inside a series root.
package cli

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/bangadam/komiku-cli/kiryuu"
	"github.com/bangadam/komiku-cli/store"
)

var readerRawPattern = regexp.MustCompile(`^[0-9]+(?:[-.][0-9]+)?$`)
var readerDisplayPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)

// readerChapterIdentity rebuilds a chapter from its on-disk identity: the
// display number and, when the directory records one, the source raw ID.
// Kiryuu bookmarks older local layouts by number alone, so a missing raw ID is
// synthesized from the display fraction ("79.2" -> "79-2").
func readerChapterIdentity(display, raw string) (kiryuu.Chapter, error) {
	if !readerDisplayPattern.MatchString(display) {
		return kiryuu.Chapter{}, fmt.Errorf("invalid chapter display %q", display)
	}
	number, err := strconv.ParseFloat(display, 64)
	if err != nil || math.IsInf(number, 0) || number > 1e9 {
		return kiryuu.Chapter{}, errors.New("chapter number exceeds reader limit")
	}
	normalized := strconv.FormatFloat(number, 'f', -1, 64)
	if raw == "" {
		raw = strings.Replace(normalized, ".", "-", 1)
	}
	if len(raw) > 80 || !readerRawPattern.MatchString(raw) {
		return kiryuu.Chapter{}, fmt.Errorf("invalid raw chapter identity %q", raw)
	}
	return kiryuu.Chapter{RawID: raw, Display: normalized, Number: number}, nil
}

type readerLocalChapter struct {
	chapter kiryuu.Chapter
	dir     string
	raw     bool
}

func (fs *readerFS) chapters() ([]readerLocalChapter, error) {
	var result []readerLocalChapter
	var walk func(string) error
	walk = func(parent string) error {
		entries, err := fs.entries(parent)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			name := entry.Name()
			path := filepath.Join(parent, name)
			if parent == "." && isVolumeDirName(name) {
				if err := walk(path); err != nil {
					return err
				}
				continue
			}
			if !strings.HasPrefix(name, "chapter-") {
				continue
			}
			parts := strings.SplitN(strings.TrimPrefix(name, "chapter-"), "-raw-", 2)
			raw := ""
			if len(parts) == 2 {
				raw = parts[1]
			}
			chapter, err := readerChapterIdentity(parts[0], raw)
			if err != nil || store.FormatChapter(chapter.Display) != parts[0] {
				continue
			}
			info, err := lstatRealRootPath(fs.root, path)
			if err != nil {
				return err
			}
			if !info.IsDir() {
				return fmt.Errorf("chapter %s is not a directory", path)
			}
			if len(result) >= maxReaderChapters {
				return errors.New("local chapter count exceeds reader limit")
			}
			result = append(result, readerLocalChapter{chapter: chapter, dir: path, raw: len(parts) == 2})
		}
		return nil
	}
	if err := walk("."); err != nil {
		return nil, err
	}
	return result, nil
}

func mergeReaderLocalChapters(chapters []kiryuu.Chapter, local []readerLocalChapter) []kiryuu.Chapter {
	for _, item := range local {
		found := false
		for _, ch := range chapters {
			if ch.Number == item.chapter.Number && (!item.raw || ch.RawID == item.chapter.RawID) {
				found = true
				break
			}
		}
		if !found {
			chapters = append(chapters, item.chapter)
		}
	}
	sort.SliceStable(chapters, func(i, j int) bool {
		if chapters[i].Number == chapters[j].Number {
			return chapters[i].RawID < chapters[j].RawID
		}
		return chapters[i].Number < chapters[j].Number
	})
	return chapters
}

func (fs *readerFS) libraryPages(ctx context.Context, local []readerLocalChapter, chapter kiryuu.Chapter, ambiguous bool) (map[int]ReaderPage, error) {
	result := map[int]ReaderPage{}
	for _, item := range local {
		if item.chapter.Number != chapter.Number || (item.raw && item.chapter.RawID != chapter.RawID) || (!item.raw && ambiguous) {
			continue
		}
		pages, err := fs.pages(ctx, item.dir)
		if err != nil {
			return nil, err
		}
		for i, page := range pages {
			if !result[i].Cached {
				result[i] = page
			}
		}
	}
	return result, nil
}

func readerImageExtension(extension string) bool {
	switch strings.ToLower(extension) {
	case ".jpg", ".jpeg", ".png", ".webp":
		return true
	}
	return false
}

// pages preserves invalid slots and rejects duplicates rather than shifting the
// following images forward. Bounds are checked before allocating page arrays.
func (fs *readerFS) pages(ctx context.Context, dir string) (map[int]ReaderPage, error) {
	entries, err := fs.entries(dir)
	if err != nil {
		return nil, err
	}
	result := map[int]ReaderPage{}
	seen := map[int]bool{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ext := filepath.Ext(entry.Name())
		if !readerImageExtension(ext) {
			continue
		}
		number, err := strconv.Atoi(strings.TrimSuffix(entry.Name(), ext))
		if err != nil || number <= 0 {
			continue
		}
		if number > store.MaxChapterPages {
			return nil, fmt.Errorf("page index %d exceeds reader limit", number)
		}
		page := ReaderPage{Index: number}
		if seen[number] {
			page.Error = "duplicate local page index"
		} else {
			relative := filepath.Join(dir, entry.Name())
			file, err := fs.open(relative)
			if err != nil {
				return nil, err
			}
			size, _, imageErr := readerImageInfo(ctx, file)
			file.Close()
			if imageErr != nil {
				page.Error = imageErr.Error()
			} else {
				page.Local, page.Bytes, page.Cached = filepath.Join(fs.base, relative), size, true
			}
		}
		seen[number] = true
		result[number] = page
	}
	return result, nil
}

// Small compatibility helpers for local callers/tests. The command uses the
// confined readerFS directly so safety errors are not discarded.
func localChapters(seriesDir string) ([]kiryuu.Chapter, error) {
	fs, err := openReaderFS(filepath.Dir(seriesDir), filepath.Base(seriesDir), false)
	if err != nil {
		return nil, err
	}
	defer fs.close()
	local, err := fs.chapters()
	if err != nil {
		return nil, err
	}
	return mergeReaderLocalChapters(nil, local), nil
}

func findLocalChapterDir(seriesDir, display string) string {
	if !readerDisplayPattern.MatchString(display) {
		return ""
	}
	fs, err := openReaderFS(filepath.Dir(seriesDir), filepath.Base(seriesDir), false)
	if err != nil {
		return ""
	}
	defer fs.close()
	local, err := fs.chapters()
	if err != nil {
		return ""
	}
	for _, item := range local {
		if item.chapter.Display == display && !item.raw {
			return filepath.Join(seriesDir, item.dir)
		}
	}
	return ""
}

func localPages(dir string) []ReaderPage {
	fs, err := openReaderFS(filepath.Dir(dir), filepath.Base(dir), false)
	if err != nil {
		return nil
	}
	defer fs.close()
	pages, err := fs.pages(context.Background(), ".")
	if err != nil {
		return nil
	}
	result := make([]ReaderPage, 0, len(pages))
	for _, page := range pages {
		if page.Cached {
			result = append(result, page)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Index < result[j].Index })
	return result
}
