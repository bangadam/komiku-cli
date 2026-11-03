// reader_manifest.go defines the reader cache manifest and validates cached chapters.
package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bangadam/komiku-cli/komiku"
	"github.com/bangadam/komiku-cli/store"
)

// There is deliberately no "complete" bit: completeness is revalidated against
// every expected slot on each read. File names are leaves, never persisted paths.
type readerManifest struct {
	Version   int                  `json:"version"`
	SeriesURL string               `json:"series_url"`
	Chapter   komiku.Chapter       `json:"chapter"`
	Chapters  []komiku.Chapter     `json:"chapters"`
	PageCount int                  `json:"page_count"`
	Pages     []readerManifestPage `json:"pages"`
}

type readerManifestPage struct {
	Index  int    `json:"index"`
	URL    string `json:"url"`
	File   string `json:"file,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

func readerCacheRelative(chapter komiku.Chapter) string {
	// Raw IDs preserve fractional aliases; URL identity also separates distinct
	// real hrefs that happen to use the same raw ID (including query variants).
	sum := sha256.Sum256([]byte(chapter.URL))
	return filepath.Join(".reader", fmt.Sprintf("chapter-%s-url-%x-raw-%s", store.FormatChapter(chapter.Display), sum[:12], chapter.RawID))
}

func readerPageStem(page readerManifestPage) string {
	sum := sha256.Sum256([]byte(page.URL))
	return fmt.Sprintf("%03d-%x", page.Index, sum[:12])
}

func validateReaderChapters(chapters []komiku.Chapter, seriesURL string) error {
	if len(chapters) == 0 || len(chapters) > maxReaderChapters {
		return errors.New("invalid or empty reader navigation list")
	}
	base, err := komiku.ValidateSeriesURL(seriesURL)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for i, ch := range chapters {
		identity, err := readerRawChapter(ch.RawID)
		if err != nil || ch.Display != identity.Display || ch.Number != identity.Number {
			return errors.New("inconsistent reader chapter identity")
		}
		u, err := url.Parse(ch.URL)
		if err != nil || len(ch.URL) > 8192 || u.User != nil || u.Scheme != base.Scheme || u.Host != base.Host || u.Opaque != "" {
			return errors.New("invalid reader chapter URL")
		}
		// Use the same scraper identity rules as real discovery, including raw
		// fractions, without treating a persisted URL as a filesystem path.
		path := strings.TrimSuffix(u.Path, "/")
		if !strings.HasPrefix(path, "/") || strings.Count(path, "/") != 1 || !strings.HasSuffix(path, "-chapter-"+ch.RawID) || strings.ContainsAny(u.Path, "\\\x00") {
			return errors.New("chapter URL does not match its raw identity")
		}
		if ch.Href != "" {
			href, err := url.Parse(ch.Href)
			if err != nil || base.ResolveReference(href).String() != ch.URL {
				return errors.New("inconsistent reader chapter href")
			}
		}
		if seen[ch.URL] || (i > 0 && chapters[i-1].Number > ch.Number) {
			return errors.New("duplicate or unordered reader navigation")
		}
		seen[ch.URL] = true
	}
	return nil
}

func validateReaderManifest(m readerManifest, seriesURL, dir string) error {
	if m.Version != readerManifestVersion || m.SeriesURL != seriesURL || m.PageCount <= 0 || m.PageCount > store.MaxChapterPages || len(m.Pages) != m.PageCount {
		return errors.New("invalid reader manifest header or expected page count")
	}
	if err := validateReaderChapters(m.Chapters, seriesURL); err != nil {
		return err
	}
	found := false
	for _, ch := range m.Chapters {
		if ch == m.Chapter {
			found = true
		}
	}
	if !found || readerCacheRelative(m.Chapter) != dir {
		return errors.New("reader manifest chapter identity does not match its directory/navigation")
	}
	for i, page := range m.Pages {
		u, err := url.Parse(page.URL)
		if page.Index != i+1 || err != nil || len(page.URL) > 8192 || u.Host == "" || u.User != nil || u.Opaque != "" || (u.Scheme != "http" && u.Scheme != "https") {
			return errors.New("invalid reader manifest page index or URL")
		}
		if page.File == "" {
			if page.Bytes != 0 || page.SHA256 != "" {
				return errors.New("reader manifest has image metadata without a file")
			}
			continue
		}
		ext := filepath.Ext(page.File)
		digest, err := hex.DecodeString(page.SHA256)
		if !readerImageExtension(ext) || page.File != readerPageStem(page)+ext || page.Bytes <= 12 || page.Bytes > maxReaderImage || err != nil || len(digest) != sha256.Size {
			return errors.New("invalid reader manifest image filename, size, or checksum")
		}
	}
	return nil
}

func (fs *readerFS) manifest(dir, seriesURL string) (readerManifest, error) {
	var manifest readerManifest
	if err := fs.readJSON(filepath.Join(dir, "manifest.json"), &manifest); err != nil {
		return readerManifest{}, err
	}
	if err := validateReaderManifest(manifest, seriesURL, dir); err != nil {
		return readerManifest{}, fmt.Errorf("invalid reader metadata in %s: %w", dir, err)
	}
	return manifest, nil
}

func (fs *readerFS) offlineMetadata(seriesURL string) ([]komiku.Chapter, map[string]readerManifest, error) {
	entries, err := fs.entries(".reader")
	if err != nil {
		return nil, nil, err
	}
	manifests := map[string]readerManifest{}
	var chapters []komiku.Chapter
	var latest time.Time
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "chapter-") {
			continue
		}
		dir := filepath.Join(".reader", entry.Name())
		m, err := fs.manifest(dir, seriesURL)
		if errors.Is(err, os.ErrNotExist) {
			continue // Pre-manifest caches cannot establish an offline page count.
		}
		if err != nil {
			return nil, nil, err
		}
		info, err := lstatRealRootPath(fs.root, filepath.Join(dir, "manifest.json"))
		if err != nil {
			return nil, nil, err
		}
		if chapters == nil || info.ModTime().After(latest) {
			latest, chapters = info.ModTime(), m.Chapters
		}
		manifests[m.Chapter.URL] = m
	}
	return chapters, manifests, nil
}

func (fs *readerFS) cachedPage(ctx context.Context, dir string, record readerManifestPage) (ReaderPage, readerManifestPage, error) {
	page := ReaderPage{Index: record.Index, URL: record.URL}
	files := []string{record.File}
	if record.File == "" {
		files = nil
		for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp"} {
			files = append(files, readerPageStem(record)+ext)
		}
	}
	original := record
	for _, name := range files {
		file, err := fs.open(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return page, record, err
		}
		size, digest, imageErr := readerImageInfo(ctx, file)
		file.Close()
		if imageErr == nil && original.File != "" && (original.Bytes != size || original.SHA256 != digest) {
			imageErr = errors.New("cached image size or checksum mismatch")
		}
		if imageErr != nil {
			page.Error = imageErr.Error()
			continue
		}
		if page.Cached {
			return ReaderPage{Index: record.Index, URL: record.URL, Error: "duplicate cached page index"}, readerManifestPage{Index: record.Index, URL: record.URL}, nil
		}
		page = ReaderPage{Index: record.Index, URL: record.URL, Local: filepath.Join(fs.base, dir, name), Cached: true, Bytes: size}
		record.File, record.Bytes, record.SHA256 = name, size, digest
	}
	// Keep an existing checksum even for a missing/corrupt file. Clearing it
	// before a failed repair would let the next run adopt corrupt bytes as an
	// unrecorded (interrupted) image.
	return page, record, nil
}
